package docker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/keller0/scr/internal/env"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"io"
	"strconv"
	"time"
)

// PayLoad as stdin pass to ric container's stdin
type PayLoad struct {
	F []*oneFile `json:"files"`
	A *argument  `json:"argument"`
	I string     `json:"stdin"`
	L string     `json:"language"`
}

type argument struct {
	Compile []string `json:"compile"`
	Run     []string `json:"run"`
}

// file type
type oneFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Worker store all infomations about the run job
type Worker struct {
	Image       string // images name
	containerID string
	tmpID       string
	cli         *client.Client
	ctx         context.Context
	// ric's stdin stdout stderr
	ricIn  io.Reader
	ricOut bytes.Buffer
	ricErr bytes.Buffer
}

var (
	MaxOutInBytes    int64 = 2 * 1024 * 1024
	ErrTooMuchOutPut       = errors.New("Too much out put")
	ErrWorkerTimeOut       = errors.New("Time out")
	memLimit               = env.Get("CONTAINER_MEM_LIMIT", "50")
	diskLimit              = env.Get("CONTAINER_DISK_LIMIT", "5")
)

// LoadInfo Load payload to worker's stdin
// language and image info from request url
func (w *Worker) LoadInfo(p *PayLoad, language, image string) error {

	p.L = language

	js, err := json.Marshal(p)
	if err != nil {
		return err
	}
	w.ricIn = bytes.NewBuffer(js)
	w.Image = image
	return nil
}

// Run start a worker
func (w *Worker) Run() (string, string, error) {

	containerJSON, err := w.createContainer()
	defer func() {
		err = w.cli.ContainerRemove(w.ctx, w.tmpID, types.ContainerRemoveOptions{})
		if err != nil {
			log.Error("failed to remove container ", w.tmpID)
		}
		log.Info("container ", w.tmpID, " removed")
	}()

	if err != nil {
		return "", "", err
	}
	w.containerID = containerJSON.ID
	err = w.attachContainer()
	if err != nil && w.ricErr.Len() == 0 {
		return "", "", err
	}

	return w.ricOut.String(), w.ricErr.String(), nil
}

func (w *Worker) createContainer() (*types.ContainerJSON, error) {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv)
	w.cli = cli
	w.ctx = ctx
	if err != nil {
		return nil, err
	}

	config := &container.Config{
		Image:        w.Image,
		Cmd:          []string{"/home/ric/run"},
		AttachStdin:  true, // Attach the standard input, makes possible user interaction
		AttachStdout: true, // Attach the standard output
		AttachStderr: true,
		Tty:          false,
		OpenStdin:    true,
		StdinOnce:    true,
	}
	ml, _ := strconv.Atoi(memLimit)
	dl, _ := strconv.Atoi(diskLimit)
	hostConfig := &container.HostConfig{
		Resources: container.Resources{
			CPUPeriod: 100000,
			CPUQuota:  50000,
			Memory:    int64(ml) * 1024 * 1024,
			PidsLimit: 50,
			// advanced kernel-level features
			// CPURealtimePeriod : 1000000,
			// CPURealtimeRuntime: 950000,

			DiskQuota: int64(dl) * 1024 * 1024,
		},
		Privileged: false,
		LogConfig: container.LogConfig{
			Type: "json-file",
		},
	}
	resp, err := cli.ContainerCreate(ctx, config, hostConfig, nil, "")
	if err != nil {
		if resp.ID != "" {
			w.tmpID = resp.ID
		}
		return nil, err
	}
	inspect, err := cli.ContainerInspect(ctx, resp.ID)
	if err != nil {
		w.tmpID = resp.ID
		return nil, err
	}
	w.containerID = resp.ID
	w.tmpID = resp.ID

	return &inspect, nil
}

func (w *Worker) attachContainer() (err error) {
	options := types.ContainerAttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	}

	log.Info("container ", w.containerID, " Attaching...")
	hijacked, err := w.cli.ContainerAttach(w.ctx, w.containerID, options)
	if err != nil {
		return
	}
	defer hijacked.Close()

	log.Info("container ", w.containerID, " Starting ...")
	err = w.cli.ContainerStart(w.ctx, w.containerID, types.ContainerStartOptions{})
	if err != nil {
		return
	}

	log.Info("container ", w.containerID, " Waiting for attach to finish...")
	attachCh := make(chan error, 2)

	// Copy any output to the build trace
	go func() {
		oc, err := stdcopy.StdCopy(&w.ricOut, &w.ricErr, hijacked.Reader)
		if oc > MaxOutInBytes {
			attachCh <- ErrTooMuchOutPut
		}
		if err != nil {
			attachCh <- err
		}
	}()

	// Write the input to the container and close its STDIN
	go func() {
		_, err := io.Copy(hijacked.Conn, w.ricIn)
		hijacked.CloseWrite()
		if err != nil {
			attachCh <- err
		}
	}()

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- w.waitForContainer()
	}()

	select {
	case <-w.ctx.Done():
		w.killContainer(w.containerID, waitCh)
		err = errors.New("Aborted")

	case err = <-attachCh:
		w.killContainer(w.containerID, waitCh)
		log.Info("container ", w.containerID, " attach finished with", err)

	case err = <-waitCh:
		log.Info("container ", w.containerID, " wait finished with", err)

	case <-time.After(10 * time.Second):
		w.killContainer(w.containerID, waitCh)
		err = ErrWorkerTimeOut
		log.Info("container ", w.containerID, " time out")
	}
	return
}

func (w *Worker) waitForContainer() error {
	log.Info("container ", w.containerID, " Waiting...")

	retries := 0
	// Use active wait
	for {
		container, err := w.cli.ContainerInspect(w.ctx, w.containerID)
		if err != nil {
			log.Info(err.Error())
			if client.IsErrNotFound(err) {
				return err
			}

			if retries > 3 {
				return err
			}

			retries++
			time.Sleep(time.Second)
			continue
		}

		// Reset retry timer
		retries = 0
		if container.State.Running {
			time.Sleep(time.Second)
			continue
		}

		if container.State.ExitCode != 0 {
			return fmt.Errorf("exit code %d", container.State.ExitCode)
		}

		return nil
	}
}

func (w *Worker) killContainer(id string, waitCh chan error) (err error) {
	for {

		log.Info("container ", id, " Killing ...")
		w.cli.ContainerKill(w.ctx, id, "SIGKILL")

		// Wait for signal that container were killed
		// or retry after some time
		select {
		case err = <-waitCh:
			return

		case <-time.After(time.Second):
		}
	}
}
