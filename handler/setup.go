// Copyright 2022 Drone.IO Inc. All rights reserved.
// Use of this source code is governed by the Polyform License
// that can be found in the LICENSE file.

package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/dchest/uniuri"
	"github.com/harness/harness-docker-runner/api"
	"github.com/harness/harness-docker-runner/engine"
	"github.com/harness/harness-docker-runner/engine/docker"
	"github.com/harness/harness-docker-runner/engine/spec"
	"github.com/harness/harness-docker-runner/executor"
	"github.com/harness/harness-docker-runner/logger"
	"github.com/harness/harness-docker-runner/pipeline"
	prruntime "github.com/harness/harness-docker-runner/pipeline/runtime"
)

// random generator function
var random = func() string {
	return uniuri.NewLen(20)
}

// HandleExecuteStep returns an http.HandlerFunc that executes a step
func HandleSetup() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st := time.Now()

		var s api.SetupRequest
		err := json.NewDecoder(r.Body).Decode(&s)
		if err != nil {
			WriteBadRequest(w, err)
			return
		}
		id := s.ID

		updateVolumes(s)

		setProxyEnvs(s.Envs)
		engine, err := engine.NewEnv(docker.Opts{})
		if err != nil {
			logger.FromRequest(r).WithError(err).Errorln("could not instantiate engine for the execution")
			WriteError(w, err)
			return
		}
		stepExecutor := prruntime.NewStepExecutor(engine)
		state := pipeline.NewState()
		// s.LogConfig.IndirectUpload = true
		// s.LogConfig.URL = "http://localhost:8079"
		state.Set(s.Volumes, s.Secrets, s.LogConfig, s.TIConfig, s.SetupRequestConfig.Network.ID)

		if s.MountDockerSocket == nil || *s.MountDockerSocket { // required to support m1 where docker isn't installed.
			s.Volumes = append(s.Volumes, getDockerSockVolume())
		}

		cfg := &spec.PipelineConfig{
			Envs:    s.Envs,
			Network: s.Network,
			Platform: spec.Platform{
				OS:   runtime.GOOS,
				Arch: runtime.GOARCH,
			},
			Volumes:           s.Volumes,
			Files:             s.Files,
			EnableDockerSetup: s.MountDockerSocket,
		}

		// Add the state of this execution to the executor
		stageData := &executor.StageData{
			Engine:       engine,
			StepExecutor: stepExecutor,
			State:        state,
		}

		ex := executor.GetExecutor()
		if err := ex.Add(id, stageData); err != nil {
			logger.FromRequest(r).WithError(err).Errorln("could not store stage data")
			WriteError(w, err)
			return
		}

		if err := engine.Setup(r.Context(), cfg); err != nil {
			logger.FromRequest(r).WithError(err).
				WithField("latency", time.Since(st)).
				WithField("time", time.Now().Format(time.RFC3339)).
				Infoln("api: failed stage setup")
			WriteError(w, err)
			ex.Remove(id)
			return
		}

		WriteJSON(w, api.SetupResponse{IPAddress: "127.0.0.1"}, http.StatusOK)
		logger.FromRequest(r).
			WithField("latency", time.Since(st)).
			WithField("time", time.Now().Format(time.RFC3339)).
			Infoln("api: successfully completed the stage setup")
	}
}

// updates the volume paths to make them compatible with the Docker runner.
// It hashes the clone path based on the runtime identifier.
func updateVolumes(r api.SetupRequest) {
	for _, v := range r.Volumes {
		if v.HostPath != nil {
			// Update the clone path to be created and removed once the build is completed
			// Hash the path with a unique identifier to avoid clashes.
			if v.HostPath.ID == "harness" {
				v.HostPath.Create = true
				v.HostPath.Remove = true
				v.HostPath.Path = v.HostPath.Path + "-" + sanitize(r.ID)
			}
		}
	}
}

func sanitize(r string) string {
	return strings.ReplaceAll(r, "[-_]", "")
}

func getDockerSockVolume() *spec.Volume {
	path := engine.DockerSockUnixPath
	if runtime.GOOS == "windows" {
		path = engine.DockerSockWinPath
	}
	return &spec.Volume{
		HostPath: &spec.VolumeHostPath{
			Name: engine.DockerSockVolName,
			Path: path,
			ID:   "docker",
		},
	}
}

func setProxyEnvs(environment map[string]string) {
	proxyEnvs := []string{"http_proxy", "https_proxy", "no_proxy", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"}
	for _, v := range proxyEnvs {
		os.Setenv(v, environment[v])
	}
}
