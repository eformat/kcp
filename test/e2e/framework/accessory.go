//go:build e2e
// +build e2e

/*
Copyright 2021 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package framework

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
)

// NewAccessory creates a new accessory process.
func NewAccessory(t TestingTInterface, artifactDir string, cmd string, args ...string) *Accessory {
	return &Accessory{
		t:           t,
		artifactDir: artifactDir,
		cmd:         cmd,
		args:        args,
	}
}

// Accessory knows how to run an executable with arguments for the duration of the context.
type Accessory struct {
	ctx         context.Context
	t           TestingTInterface
	artifactDir string
	cmd         string
	args        []string
}

func (a *Accessory) Run(parentCtx context.Context) error {
	ctx, cancel := context.WithCancel(parentCtx)

	if deadline, ok := a.t.Deadline(); ok {
		deadlinedCtx, deadlinedCancel := context.WithDeadline(ctx, deadline.Add(-10*time.Second))
		ctx = deadlinedCtx
		a.t.Cleanup(deadlinedCancel) // this does not really matter but govet is upset
	}
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	a.t.Cleanup(func() {
		a.t.Logf("cleanup: ending `%s`", a.cmd)
		cancel()
		<-cleanupCtx.Done()
	})

	a.ctx = ctx
	cmd := exec.CommandContext(ctx, a.cmd, a.args...)

	a.t.Logf("running: %v", strings.Join(cmd.Args, " "))
	logFile, err := os.Create(filepath.Join(a.artifactDir, fmt.Sprintf("%s.log", a.cmd)))
	if err != nil {
		cleanupCancel()
		return fmt.Errorf("could not create log file: %w", err)
	}
	log := bytes.Buffer{}
	writers := []io.Writer{&log, logFile}
	mw := io.MultiWriter(writers...)
	cmd.Stdout = mw
	cmd.Stderr = mw
	if err := cmd.Start(); err != nil {
		cleanupCancel()
		return err
	}
	go func() {
		defer func() { cleanupCancel() }()
		err := cmd.Wait()
		if err != nil && ctx.Err() == nil {
			a.t.Errorf("`%s` failed: %w output: %s", a.cmd, err, log)
		}
	}()
	return nil
}

// Ready blocks until the server is healthy and ready.
func Ready(ctx context.Context, t TestingTInterface, port string) bool {
	wg := sync.WaitGroup{}
	wg.Add(2)
	for _, endpoint := range []string{"/healthz", "/readyz"} {
		go func(endpoint string) {
			defer wg.Done()
			waitForEndpoint(ctx, t, port, endpoint)
		}(endpoint)
	}
	wg.Wait()
	return !t.Failed()
}

func waitForEndpoint(ctx context.Context, t TestingTInterface, port, endpoint string) {
	var lastMsg string
	var succeeded bool
	loadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	wait.UntilWithContext(loadCtx, func(ctx context.Context) {
		url := fmt.Sprintf("http://[::1]:%s%s", port, endpoint)
		resp, err := http.Get(url)
		if err == nil {
			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				lastMsg = fmt.Sprintf("error reading response from %s: %v", url, err)
				return
			}
			if resp.StatusCode != 200 {
				lastMsg = fmt.Sprintf("unready response from %s: %v", url, string(body))
				return
			}
			t.Logf("success contacting %s", url)
			cancel()
			succeeded = true
		} else {
			lastMsg = fmt.Sprintf("error contacting %s: %v", url, err)
		}
	}, 100*time.Millisecond)
	if !succeeded {
		t.Errorf(lastMsg)
	}
}
