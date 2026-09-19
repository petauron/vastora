package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
)

func (c Client) post(ctx context.Context, endpoint string, payload any, credential, caFingerprint, caCertificatePEM string, target any) (err error) {
	return c.postLimit(ctx, endpoint, payload, credential, caFingerprint, caCertificatePEM, target, controlplane.MaxJSONPayload)
}

func (c Client) postLimit(ctx context.Context, endpoint string, payload any, credential, caFingerprint, caCertificatePEM string, target any, limit int) (err error) {
	defer c.abortExecutionOnError(&err)
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("agent: encode Center request: %w", err)
	}
	if len(body) > limit {
		return errors.New("agent: Center request exceeds the allowed size")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("agent: create Center request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	statusCode, status, content, err := c.doCenterRequest(request, caFingerprint, caCertificatePEM)
	if err != nil {
		return err
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(content, &failure)
		if failure.Error == "" {
			failure.Error = status
		}
		return &centerResponseError{status: statusCode, message: failure.Error}
	}
	if target != nil && json.Unmarshal(content, target) != nil {
		return errors.New("agent: Center returned invalid JSON")
	}
	return nil
}

func (c Client) get(ctx context.Context, endpoint, credential, caFingerprint, caCertificatePEM string, target any) (err error) {
	defer c.abortExecutionOnError(&err)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("agent: create Center request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("X-Vastora-Execution-Session", c.executionSession)
	statusCode, status, content, err := c.doCenterRequest(request, caFingerprint, caCertificatePEM)
	if err != nil {
		return err
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("agent: Center request failed: %s", status)
	}
	if err := json.Unmarshal(content, target); err != nil {
		return errors.New("agent: Center returned invalid JSON")
	}
	return nil
}

func (c Client) abortExecutionOnError(err *error) {
	if *err != nil && c.executionAbort != nil {
		c.executionAbort(*err)
	}
}

func (c Client) doCenterRequest(request *http.Request, caFingerprint, caCertificatePEM string) (statusCode int, status string, content []byte, err error) {
	client, release, err := c.clientFor(caFingerprint, caCertificatePEM, 15*time.Second)
	if err != nil {
		return 0, "", nil, err
	}
	defer release()
	response, err := client.Do(request)
	if err != nil {
		return 0, "", nil, fmt.Errorf("agent: request Center: %w", err)
	}
	defer response.Body.Close()
	content, err = readBoundedResponse(response.Body, controlplane.MaxEnvelopeWire)
	if err != nil {
		return 0, "", nil, fmt.Errorf("agent: read Center response: %w", err)
	}
	return response.StatusCode, response.Status, content, nil
}

func readBoundedResponse(reader io.Reader, limit int64) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, errors.New("agent: Center response exceeds the allowed size")
	}
	return content, nil
}
