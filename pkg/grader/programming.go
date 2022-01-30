package grader

import (
	model_pb "autograder-server/pkg/model/proto"
	"context"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"io"
	"os"
)

type ProgrammingGrader interface {
	GradeSubmission(submission *model_pb.Submission, config *model_pb.ProgrammingAssignmentConfig)
}

type DockerProgrammingGrader struct {
	cli *client.Client
}

func (d *DockerProgrammingGrader) GradeSubmission(submission *model_pb.Submission, config *model_pb.ProgrammingAssignmentConfig) {
	closer, err := d.cli.ImagePull(context.Background(), config.Image, types.ImagePullOptions{})
	if err != nil {
		panic(err)
	}
	io.Copy(os.Stdout, closer)
	ctCfg := &container.Config{
		Hostname:     "",
		Domainname:   "",
		User:         "",
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		OpenStdin:    true,
		StdinOnce:    false,
		Env:          nil,
		Cmd:          []string{"touch", "a.txt"},
		Image:        config.Image,
		Volumes:      nil,
		WorkingDir:   "/autograder",
	}
	cwd, _ := os.Getwd()
	hstCfg := &container.HostConfig{
		Mounts: []mount.Mount{{Type: mount.TypeBind, Source: cwd, Target: "/autograder"}},
	}
	netCfg := &network.NetworkingConfig{}
	platform := &specs.Platform{Architecture: "amd64", OS: "linux"}
	body, err := d.cli.ContainerCreate(context.Background(), ctCfg, hstCfg, netCfg, platform, "")
	if err != nil {
		panic(err)
	}
	id := body.ID
	doneC, errC := d.cli.ContainerWait(context.Background(), id, container.WaitConditionNextExit)
	err = d.cli.ContainerStart(context.Background(), id, types.ContainerStartOptions{})
	if err != nil {
		panic(err)
	}
	select {
	case err := <-errC:
		panic(err)
	case <-doneC:

	}
}

func NewDockerProgrammingGrader() ProgrammingGrader {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		panic(err)
	}
	return &DockerProgrammingGrader{cli: cli}
}
