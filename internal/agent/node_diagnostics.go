package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/nodediagnostics"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

const (
	networkProbeTimeout  = 2 * time.Second
	routeProbeTimeout    = 800 * time.Millisecond
	routeMaxHops         = 30
	bandwidthTimeout     = 20 * time.Second
	bandwidthTaskTimeout = 2 * time.Minute
	bandwidthOutputMax   = 64 * 1024
)

var errBandwidthEndpointBusy = errors.New("agent: bandwidth endpoint busy")

func (e ApplicationExecutor) CheckNetworkQuality(ctx context.Context, task nodediagnostics.Task) (nodediagnostics.Result, error) {
	if err := task.Validate(); err != nil {
		return nodediagnostics.Result{}, err
	}
	result := nodediagnostics.Result{Network: make([]nodediagnostics.NetworkMeasurement, 0, len(task.Targets))}
	for _, target := range task.Targets {
		measurement := nodediagnostics.NetworkMeasurement{Carrier: target.Carrier}
		host, port, _ := net.SplitHostPort(target.Address)
		address, resolveErr := resolvePublicIPv4(ctx, host)
		if resolveErr != nil {
			measurement.Loss = 100
			result.Network = append(result.Network, measurement)
			continue
		}
		destination := net.JoinHostPort(address.String(), port)
		values := make([]float64, 0, nodediagnostics.ProbeCount)
		for attempt := 0; attempt < nodediagnostics.ProbeCount; attempt++ {
			probe, cancel := context.WithTimeout(ctx, networkProbeTimeout)
			started := time.Now()
			connection, err := (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(task.BindAddress)}}).DialContext(probe, "tcp", destination)
			if err == nil {
				values = append(values, float64(time.Since(started).Microseconds())/1000)
				_ = connection.Close()
			}
			cancel()
			if ctx.Err() != nil {
				return nodediagnostics.Result{Error: "timeout"}, nil
			}
		}
		measurement.Loss = float64(nodediagnostics.ProbeCount-len(values)) * 100 / nodediagnostics.ProbeCount
		if len(values) > 0 {
			for _, value := range values {
				measurement.Latency += value
			}
			measurement.Latency /= float64(len(values))
			for index := 1; index < len(values); index++ {
				measurement.Jitter += math.Abs(values[index] - values[index-1])
			}
			if len(values) > 1 {
				measurement.Jitter /= float64(len(values) - 1)
			}
		}
		result.Network = append(result.Network, measurement)
	}
	return result, nil
}

func (e ApplicationExecutor) CheckReturnRoutes(ctx context.Context, task nodediagnostics.Task) (nodediagnostics.Result, error) {
	if err := task.Validate(); err != nil {
		return nodediagnostics.Result{}, err
	}
	result := nodediagnostics.Result{Routes: make([]nodediagnostics.Route, 0, len(task.Targets))}
	for _, target := range task.Targets {
		host, _, _ := net.SplitHostPort(target.Address)
		address, err := resolvePublicIPv4(ctx, host)
		if err != nil {
			return nodediagnostics.Result{Error: "probe_failed"}, nil
		}
		route, err := traceICMPRoute(ctx, task.BindAddress, address, target.Carrier)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nodediagnostics.Result{Error: "timeout"}, nil
			}
			return nodediagnostics.Result{Error: "tool_unavailable"}, nil
		}
		result.Routes = append(result.Routes, route)
	}
	return result, nil
}

func (e ApplicationExecutor) CheckInternationalBandwidth(ctx context.Context, task nodediagnostics.Task) (nodediagnostics.Result, error) {
	if err := task.ValidateBandwidth(); err != nil {
		return nodediagnostics.Result{}, err
	}
	executable, err := exec.LookPath("iperf3")
	if err != nil {
		return nodediagnostics.Result{Error: "tool_unavailable"}, nil
	}
	taskContext, cancel := context.WithTimeout(ctx, bandwidthTaskTimeout)
	defer cancel()
	result := nodediagnostics.Result{Bandwidth: make([]nodediagnostics.BandwidthMeasurement, 0, len(task.BandwidthTargets)*2)}
	for _, target := range task.BandwidthTargets {
		address, err := resolvePublicIPv4(taskContext, target.Host)
		if err != nil {
			for _, direction := range []string{"download", "upload"} {
				result.Bandwidth = append(result.Bandwidth, nodediagnostics.BandwidthMeasurement{Region: target.Region, Location: target.Location, Direction: direction, State: "failed", Error: "probe_failed"})
			}
			continue
		}
		for _, direction := range []string{"download", "upload"} {
			measurement, err := runIPerf3Sample(taskContext, executable, task.BindAddress, address.String(), target, direction)
			if err != nil {
				if taskContext.Err() != nil {
					return nodediagnostics.Result{Error: "timeout"}, nil
				}
				if errors.Is(err, errBandwidthEndpointBusy) {
					measurement.State, measurement.Error = "unavailable", "endpoint_busy"
				} else {
					measurement.State, measurement.Error = "failed", "probe_failed"
				}
			}
			result.Bandwidth = append(result.Bandwidth, measurement)
		}
	}
	return result, nil
}

type boundedCommandOutput struct {
	bytes.Buffer
	limit int
}

func (w *boundedCommandOutput) Write(value []byte) (int, error) {
	if w.Len()+len(value) > w.limit {
		return 0, errors.New("agent: diagnostic output exceeded limit")
	}
	return w.Buffer.Write(value)
}

type iperfJSONResult struct {
	Error string `json:"error"`
	End   struct {
		SumSent struct {
			Bytes         int64   `json:"bytes"`
			Seconds       float64 `json:"seconds"`
			BitsPerSecond float64 `json:"bits_per_second"`
		} `json:"sum_sent"`
		SumReceived struct {
			Bytes         int64   `json:"bytes"`
			Seconds       float64 `json:"seconds"`
			BitsPerSecond float64 `json:"bits_per_second"`
		} `json:"sum_received"`
	} `json:"end"`
}

func runIPerf3Sample(ctx context.Context, executable, bindAddress, address string, target nodediagnostics.BandwidthTarget, direction string) (nodediagnostics.BandwidthMeasurement, error) {
	measurement := nodediagnostics.BandwidthMeasurement{Region: target.Region, Location: target.Location, Direction: direction}
	var lastErr error
	for port := 5201; port <= 5203; port++ {
		sampleContext, cancel := context.WithTimeout(ctx, bandwidthTimeout)
		arguments := []string{"-c", address, "-p", strconv.Itoa(port), "-4", "-B", bindAddress, "-J", "-n", strconv.FormatInt(nodediagnostics.BandwidthBytes, 10)}
		if direction == "download" {
			arguments = append(arguments, "-R")
		}
		command := exec.CommandContext(sampleContext, executable, arguments...)
		output := &boundedCommandOutput{limit: bandwidthOutputMax}
		command.Stdout, command.Stderr = output, output
		err := command.Run()
		contextErr := sampleContext.Err()
		cancel()
		if contextErr != nil {
			return measurement, contextErr
		}
		var report iperfJSONResult
		if json.Unmarshal(output.Bytes(), &report) != nil {
			lastErr = fmt.Errorf("agent: invalid iperf3 output")
			continue
		}
		if err != nil || report.Error != "" {
			message := strings.ToLower(report.Error + " " + output.String())
			if strings.Contains(message, "server is busy") || strings.Contains(message, "unable to connect") {
				lastErr = errBandwidthEndpointBusy
				continue
			}
			return measurement, errors.New("agent: iperf3 sample failed")
		}
		value := report.End.SumSent
		if direction == "download" {
			value = report.End.SumReceived
		}
		if value.Bytes < 0 || value.Bytes > nodediagnostics.BandwidthBytes || value.Seconds <= 0 || value.Seconds > 30 || value.BitsPerSecond < 0 {
			return measurement, errors.New("agent: invalid iperf3 measurement")
		}
		measurement.State = "completed"
		measurement.Bytes, measurement.Duration, measurement.Megabits = value.Bytes, value.Seconds, value.BitsPerSecond/1_000_000
		return measurement, nil
	}
	return measurement, lastErr
}

func traceICMPRoute(ctx context.Context, bindAddress string, destination net.IP, carrier string) (nodediagnostics.Route, error) {
	connection, err := icmp.ListenPacket("ip4:icmp", bindAddress)
	if err != nil {
		return nodediagnostics.Route{}, err
	}
	defer connection.Close()
	packet := connection.IPv4PacketConn()
	route := nodediagnostics.Route{Carrier: carrier, StopReason: "max_hops", Hops: make([]nodediagnostics.Hop, 0, routeMaxHops)}
	identifier := os.Getpid() & 0xffff
	buffer := make([]byte, 1500)
	for ttl := 1; ttl <= routeMaxHops; ttl++ {
		if err := ctx.Err(); err != nil {
			return nodediagnostics.Route{}, err
		}
		if err := packet.SetTTL(ttl); err != nil {
			return nodediagnostics.Route{}, err
		}
		message := icmp.Message{Type: ipv4.ICMPTypeEcho, Code: 0, Body: &icmp.Echo{ID: identifier, Seq: ttl, Data: []byte("vastora")}}
		encoded, err := message.Marshal(nil)
		if err != nil {
			return nodediagnostics.Route{}, err
		}
		started := time.Now()
		if _, err := connection.WriteTo(encoded, &net.IPAddr{IP: destination}); err != nil {
			return nodediagnostics.Route{}, err
		}
		deadline := time.Now().Add(routeProbeTimeout)
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
			deadline = contextDeadline
		}
		_ = connection.SetReadDeadline(deadline)
		count, peer, readErr := connection.ReadFrom(buffer)
		hop := nodediagnostics.Hop{TTL: ttl}
		if readErr == nil {
			if address, ok := peer.(*net.IPAddr); ok {
				hop.Address = address.IP.String()
			}
			hop.Latency = float64(time.Since(started).Microseconds()) / 1000
			response, parseErr := icmp.ParseMessage(1, buffer[:count])
			if parseErr == nil && response.Type == ipv4.ICMPTypeEchoReply {
				route.StopReason = "destination_reached"
				route.Hops = append(route.Hops, hop)
				return route, nil
			}
			if parseErr == nil && response.Type == ipv4.ICMPTypeDestinationUnreachable {
				route.StopReason = "unreachable"
				route.Hops = append(route.Hops, hop)
				return route, nil
			}
		} else if !isTimeout(readErr) {
			return nodediagnostics.Route{}, readErr
		}
		route.Hops = append(route.Hops, hop)
	}
	return route, nil
}

func isTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func resolvePublicIPv4(ctx context.Context, host string) (net.IP, error) {
	addresses, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return nil, err
	}
	for _, address := range addresses {
		if address.To4() != nil && address.IsGlobalUnicast() && !address.IsPrivate() {
			return address, nil
		}
	}
	return nil, errors.New("agent: diagnostic target did not resolve to public IPv4")
}
