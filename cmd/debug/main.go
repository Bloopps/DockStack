package main

import (
	"context"
	"fmt"
	"io"

	dockercli "github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	mobyclient "github.com/moby/moby/client"
)

func main() {
	cli, _ := dockercli.NewDockerCli(
		dockercli.WithOutputStream(io.Discard),
		dockercli.WithErrorStream(io.Discard),
	)
	cli.Initialize(flags.NewClientOptions())
	result, err := cli.Client().ContainerList(context.Background(), mobyclient.ContainerListOptions{All: true})
	if err != nil {
		fmt.Println("ERROR:", err)
		return
	}
	fmt.Printf("Total containers: %d\n", len(result.Items))
	for _, ctr := range result.Items {
		proj := ctr.Labels["com.docker.compose.project"]
		wd   := ctr.Labels["com.docker.compose.project.working_dir"]
		name := ""
		if len(ctr.Names) > 0 { name = ctr.Names[0] }
		fmt.Printf("name=%-30s state=%-12s project=%-20s wd=%s\n", name, ctr.State, proj, wd)
	}
}
