package update

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Opt-in real-process acceptance: binaries are built from this checkout with
// different versions. No installed user service or normal data directory is used.
func TestRealProcessUpdateAndRollback(t *testing.T) {
	oldBinary, newBinary := os.Getenv("SURGEEB_UPDATE_TEST_OLD"), os.Getenv("SURGEEB_UPDATE_TEST_NEW")
	if oldBinary == "" || newBinary == "" {
		t.Skip("set SURGEEB_UPDATE_TEST_OLD/NEW to run real process acceptance")
	}
	for _, forceFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "upgrade", true: "rollback"}[forceFailure], func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "seb-up-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			freePort := func() int {
				l, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
				return l.Addr().(*net.TCPAddr).Port
			}
			port, socks := freePort(), freePort()
			config := map[string]any{"schema_version": 5, "mode": "local", "http_bind": net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), "socks_bind": "127.0.0.1", "socks_port": socks, "socks_host": "127.0.0.1", "policy_host": "127.0.0.1", "projection_key": "test-integration-projection-key", "prefix_provider": true, "projection_types": []string{"*"}, "node_test_url": "https://www.gstatic.com/generate_204", "node_test_udp_address": "8.8.8.8:53", "node_test_timeout_seconds": 15, "providers": []any{}, "policy_paths": []any{map[string]any{"id": "default", "name": "all", "include_all": true}}}
			data, _ := json.Marshal(config)
			if err = os.WriteFile(filepath.Join(dir, "gateway.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			target, staged := filepath.Join(dir, "SurgeEB"), filepath.Join(dir, "candidate")
			if err = copyFile(oldBinary, target, 0o755); err != nil {
				t.Fatal(err)
			}
			if err = copyFile(newBinary, staged, 0o755); err != nil {
				t.Fatal(err)
			}
			var process *exec.Cmd
			log, _ := os.Create(filepath.Join(dir, "process.log"))
			defer log.Close()
			start := func() error {
				process = exec.Command(target, "serve", "--data-dir", dir)
				process.Stdout = log
				process.Stderr = log
				return process.Start()
			}
			stop := func() error {
				if process == nil {
					return nil
				}
				_ = process.Process.Signal(os.Interrupt)
				done := make(chan error, 1)
				go func() { done <- process.Wait() }()
				select {
				case <-done:
					process = nil
					return nil
				case <-time.After(15 * time.Second):
					_ = process.Process.Kill()
					<-done
					process = nil
					return errors.New("shutdown timed out")
				}
			}
			defer stop()
			j := Journal{ID: "acceptance", DataDir: dir, Target: target, Staged: staged, Version: "v1.0.1", PreviousVersion: "1.0.0", Service: true, HealthURL: healthURL(config["http_bind"].(string)), HealthHost: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
			if err = start(); err != nil {
				t.Fatal(err)
			}
			if err = waitReady(j, "1.0.0"); err != nil {
				logs, _ := os.ReadFile(log.Name())
				t.Fatalf("%v\n%s", err, logs)
			}
			// Initial startup may migrate the fixture schema. The transaction
			// must preserve the configuration accepted by the running version.
			beforeUpdate, err := os.ReadFile(filepath.Join(dir, "gateway.json"))
			if err != nil {
				t.Fatal(err)
			}
			oldHealth, _ := readHealth(j)
			j.InstanceID = oldHealth.InstanceID
			j.PID = oldHealth.PID
			j.Digest, _ = fileDigest(staged)
			j.PreviousDigest, _ = fileDigest(target)
			ready := func(version string) error {
				if err := waitReady(j, version); err != nil {
					return err
				}
				if forceFailure && version == j.Version {
					return errors.New("injected readiness rejection after real new process started")
				}
				return nil
			}
			err = runTransaction(&j, transactionOps{stop: stop, start: start, ready: ready})
			if forceFailure {
				if err == nil || j.Phase != "rolled_back" {
					t.Fatalf("rollback: %+v %v", j, err)
				}
			} else if err != nil || j.Phase != "succeeded" {
				t.Fatalf("upgrade: %+v %v", j, err)
			}
			current, _ := readHealth(j)
			want := "1.0.1"
			if forceFailure {
				want = "1.0.0"
			}
			if current.Version != want || !current.OK {
				t.Fatalf("health=%+v", current)
			}
			after, _ := os.ReadFile(filepath.Join(dir, "gateway.json"))
			if string(after) != string(beforeUpdate) {
				t.Fatal("configuration changed")
			}
		})
	}
}
