//go:build linux

package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const budgetKernel = "/proc/sys/net/ipv4/tcp_mem"
const budgetDropIn = "/etc/sysctl.d/99-glivela-tcp-budget.conf"
const budgetSocket = "/run/vastora-tcp-budget/control.sock"

type budgetRequest struct {
	Action   string    `json:"action"`
	Expected string    `json:"expected,omitempty"`
	ID       string    `json:"id,omitempty"`
	Expires  time.Time `json:"expiresAt,omitzero"`
}
type budgetView struct {
	PageBytes int64    `json:"pageBytes"`
	Current   [3]int64 `json:"currentPages"`
	Tier      int64    `json:"tierMiB,omitempty"`
	Target    [3]int64 `json:"targetMiB"`
	Apply     bool     `json:"canApply,omitempty"`
	Restore   bool     `json:"canRestore,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Revision  string   `json:"revision"`
}
type budgetRecord struct {
	Owner, Command, Stage string
	Original, Target      [3]int64
	PageBytes             int64
}

func (r *budgetRecord) content() []byte {
	return []byte(fmt.Sprintf("# Managed by Vastora TCP budget %x\nnet.ipv4.tcp_mem = %d %d %d\n", sha256.Sum256([]byte(r.Owner)), r.Target[0], r.Target[1], r.Target[2]))
}
func budgetOrdered(a [3]int64) bool { return a[0] > 0 && a[0] <= a[1] && a[1] <= a[2] }
func budgetRead() (p [3]int64, err error) {
	b, e := os.ReadFile(budgetKernel)
	if e != nil {
		return p, e
	}
	f := strings.Fields(string(b))
	if len(f) != 3 {
		return p, errors.New("invalid host TCP budget")
	}
	for i, s := range f {
		p[i], err = strconv.ParseInt(s, 10, 64)
		if err != nil {
			return p, err
		}
	}
	if !budgetOrdered(p) {
		return p, errors.New("invalid kernel watermarks")
	}
	return
}
func budgetTier() (int64, [3]int64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) != 3 || f[0] != "MemTotal:" || f[2] != "kB" {
				continue
			}
			kb, _ := strconv.ParseInt(f[1], 10, 64)
			for _, row := range [][4]int64{{512, 64, 96, 128}, {1024, 64, 128, 256}, {2048, 128, 256, 512}} {
				if kb >= row[0]*896 && kb <= row[0]*1024 {
					return row[0], [3]int64{row[1], row[2], row[3]}
				}
			}
		}
	}
	return 0, [3]int64{}
}
func budgetSafeParents(file string) error {
	for dir := filepath.Dir(file); dir != "/"; dir = filepath.Dir(dir) {
		x, e := os.Lstat(dir)
		if e != nil || !x.IsDir() || x.Mode().Perm()&0022 != 0 || x.Sys().(*syscall.Stat_t).Uid != 0 {
			return errors.New("unsafe TCP budget directory")
		}
	}
	return nil
}
func budgetFile(path string, mode os.FileMode) ([]byte, error) {
	if e := budgetSafeParents(path); e != nil {
		return nil, e
	}
	x, e := os.Lstat(path)
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil || !x.Mode().IsRegular() || x.Mode().Perm() != mode || x.Sys().(*syscall.Stat_t).Uid != 0 || x.Size() > 2048 {
		return nil, errors.New("unowned TCP budget file")
	}
	return os.ReadFile(path)
}
func budgetSave(path string, b []byte, mode os.FileMode, create bool) error {
	if e := budgetSafeParents(path); e != nil {
		return e
	}
	dir := filepath.Dir(path)
	f, e := os.CreateTemp(dir, ".vastora-budget-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, e = f.Write(b); e != nil {
		return e
	}
	if e = f.Chmod(mode); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if create {
		e = os.Link(f.Name(), path)
	} else {
		e = os.Rename(f.Name(), path)
	}
	if e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func budgetInspect(dir, owner string) (v budgetView, r *budgetRecord, exists bool, err error) {
	v.PageBytes = int64(os.Getpagesize())
	v.Current, err = budgetRead()
	if err != nil {
		return
	}
	v.Tier, v.Target = budgetTier()
	data, e := budgetFile(filepath.Join(dir, "host-tcp-budget.json"), 0600)
	if e != nil {
		err = e
		return
	}
	var rec budgetRecord
	if data != nil {
		if json.Unmarshal(data, &rec) != nil || !budgetOrdered(rec.Original) || !budgetOrdered(rec.Target) || rec.PageBytes != v.PageBytes {
			err = errors.New("invalid host recovery record")
			return
		}
		r = &rec
	}
	drop, e := budgetFile(budgetDropIn, 0644)
	if e != nil {
		err = e
		return
	}
	exists = drop != nil
	if exists && (r == nil || r.Stage == "restored" || !bytes.Equal(drop, r.content())) {
		err = errors.New("persisted TCP budget is not owned or changed externally")
		return
	}
	if r == nil || r.Stage == "restored" {
		v.Apply = v.Tier != 0 && v.PageBytes > 0 && (1<<20)%v.PageBytes == 0
	} else if r.Owner == owner {
		switch r.Stage {
		case "applied":
			v.Restore = exists && v.Current == r.Target
		case "applying", "restoring":
			v.Restore = v.Current == r.Original || v.Current == r.Target
		default:
			err = errors.New("invalid host recovery stage")
			return
		}
	}
	if !v.Apply && !v.Restore {
		v.Reason = "内存档位不适用、预算归属不符或主机状态已变化"
	}
	encoded, _ := json.Marshal(struct {
		Owner  string
		V      budgetView
		R      *budgetRecord
		Exists bool
	}{owner, v, r, exists})
	digest := sha256.Sum256(encoded)
	v.Revision = hex.EncodeToString(digest[:])
	return
}
func hostTCPBudget(dir, owner string, q budgetRequest) (budgetView, error) {
	v, r, exists, err := budgetInspect(dir, owner)
	if err != nil {
		return v, err
	}
	if q.Action == "inspect" && q.Expected == "" && q.ID == "" && q.Expires.IsZero() {
		return v, nil
	}
	restoring := q.Action == "restore"
	if (!restoring && q.Action != "apply") || q.Expected != v.Revision || q.ID == "" || len(q.ID) > 128 || !time.Now().Before(q.Expires) || q.Expires.After(time.Now().Add(10*time.Minute)) || (restoring && !v.Restore) || (!restoring && !v.Apply) {
		return v, errors.New("stale or invalid TCP budget operation")
	}
	if restoring {
		r.Stage = "restoring"
		r.Command = q.ID
	} else {
		r = &budgetRecord{Owner: owner, Stage: "applying", Command: q.ID, Original: v.Current, PageBytes: v.PageBytes}
		for i, t := range v.Target {
			r.Target[i] = t * (1 << 20) / v.PageBytes
		}
	}
	save := func() error {
		b, _ := json.Marshal(r)
		return budgetSave(filepath.Join(dir, "host-tcp-budget.json"), b, 0600, false)
	}
	if err = save(); err != nil {
		return v, err
	}
	want := r.Target
	if restoring {
		want = r.Original
	} else if err = budgetSave(budgetDropIn, r.content(), 0644, true); err != nil {
		return v, err
	}
	read, e := budgetRead()
	if e != nil || read != v.Current {
		return v, errors.New("kernel budget changed before write")
	}
	if read != want {
		if err = os.WriteFile(budgetKernel, []byte(fmt.Sprintf("%d %d %d\n", want[0], want[1], want[2])), 0); err != nil {
			return v, err
		}
	}
	read, e = budgetRead()
	if e != nil || read != want {
		return v, errors.New("kernel readback mismatch")
	}
	disk, e := budgetFile(budgetDropIn, 0644)
	if e != nil || ((!restoring || exists) && !bytes.Equal(disk, r.content())) || (restoring && !exists && disk != nil) {
		return v, errors.New("persistent budget changed")
	}
	if restoring {
		if exists {
			if err = os.Remove(budgetDropIn); err != nil {
				return v, err
			}
			d, e := os.Open(filepath.Dir(budgetDropIn))
			if e != nil {
				return v, e
			}
			e = d.Sync()
			d.Close()
			if e != nil {
				return v, e
			}
		}
		r.Stage = "restored"
	} else {
		r.Stage = "applied"
	}
	if err = save(); err != nil {
		return v, err
	}
	v, _, _, err = budgetInspect(dir, owner)
	return v, err
}
