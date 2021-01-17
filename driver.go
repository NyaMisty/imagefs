package main

import (
	"bufio"
	"context"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/versions"
	"github.com/docker/docker/client"
	"github.com/docker/go-plugins-helpers/volume"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"strings"
	"time"
)

// ImagefsDriver implements the docker volume driver interface to
// mount images as volumes and commit volume diffs as image layers
type ImagefsDriver struct {
	cli *client.Client
}

// Create creates a volume
func (d ImagefsDriver) Create(r *volume.CreateRequest) error {
	fmt.Printf("-> Create %+v\n", r)
	source, ok := r.Options["source"]
	if !ok {
		return fmt.Errorf("no source volume specified")
	}

	// pull the image
	/*readCloser, err := d.cli.ImagePull(context.Background(), source, types.ImagePullOptions{
		// HACK assume the registry ignores the auth header
		RegistryAuth: "null",
	})
	if err != nil {
		return fmt.Errorf("unexpected error: %s", err)
	}
	scanner := bufio.NewScanner(readCloser)
	for scanner.Scan() {
	}*/

	containerConfig := &container.Config{
		Image:      source,
		Entrypoint: []string{"/runtime/loop"},
		Labels: map[string]string{
			"com.docker.imagefs.version": version,
		},
		NetworkDisabled: true,
	}

	if target, ok := r.Options["target"]; ok {
		containerConfig.Labels["com.docker.imagefs.target"] = target
	}
	// TODO handle error
	hostConfig := &container.HostConfig{
		Binds: []string{"/tmp/runtime:/runtime"},
		//AutoRemove: true,
	}

	var platform *specs.Platform
	if platformStr, ok := r.Options["platform"]; ok {
		if versions.GreaterThanOrEqualTo(d.cli.ClientVersion(), "1.41") {
			p, err := platforms.Parse(platformStr)
			if err != nil {
				return errors.Wrap(err, "error parsing specified platform")
			}
			platform = &p
		}
	}

	networkConfig := &network.NetworkingConfig{}
	cont, err := d.cli.ContainerCreate(
		context.Background(),
		containerConfig,
		hostConfig,
		networkConfig,
		platform,
		// TODO(rabrams) namespace
		r.Name,
	)
	if err != nil {
		return fmt.Errorf("unexpected error: %s", err)
	}
	fmt.Printf("Temp container ID: %s", cont.ID)
	d.cli.ContainerStart(
		context.Background(),
		cont.ID,
		types.ContainerStartOptions{},
	)
	if err != nil {
		return fmt.Errorf("unexpected error: %s", err)
	}
	return nil
}

func (d ImagefsDriver) FindVolumeContainer(volName string) (string, error) {
	containers, err := d.cli.ContainerList(
		context.Background(),
		types.ContainerListOptions{
			All:     true,
			Limit:   1,
			Filters: filters.NewArgs(filters.Arg("name", volName), filters.Arg("label", "com.docker.imagefs.version")),
		},
	)
	if err != nil {
		return "", fmt.Errorf("unexpected error: %s", err)
	}
	if len(containers) != 1 {
		return "", fmt.Errorf("volume not found: %s", volName)
	}
	return containers[0].ID, nil
}

// Get gets a volume
func (d ImagefsDriver) Get(r *volume.GetRequest) (*volume.GetResponse, error) {
	fmt.Printf("-> Get %+v\n", r)
	containerID, err := d.FindVolumeContainer(r.Name)
	container, err := d.cli.ContainerInspect(context.Background(), containerID)
	if err != nil {
		return nil, fmt.Errorf("unexpected error: %s", err)
	}
	if container.GraphDriver.Name != "overlay" && container.GraphDriver.Name != "overlay2" {
		return nil, fmt.Errorf("unexpected graph driver: %s", container.GraphDriver.Name)
	}
	mergedDir, ok := container.GraphDriver.Data["MergedDir"]
	if !ok {
		return nil, fmt.Errorf("missing MergedDir")
	}
	fmt.Printf("Got mergeDir: %s\n", mergedDir)
	// HACK directory is relative to host but docker will prepend rootfs of the
	// plugin container
	mergedDir = fmt.Sprintf("../../../../../../../../../../../%s", mergedDir)
	return &volume.GetResponse{
		Volume: &volume.Volume{
			Name:       r.Name,
			Mountpoint: mergedDir,
		},
	}, nil
}

// List lists available volumes
func (d ImagefsDriver) List() (*volume.ListResponse, error) {
	containers, err := d.cli.ContainerList(context.Background(), types.ContainerListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "com.docker.imagefs.version")),
	})
	if err != nil {
		return nil, fmt.Errorf("unexpected error: %s", err)
	}
	response := &volume.ListResponse{}
	for i := range containers {
		response.Volumes = append(response.Volumes, &volume.Volume{
			// TODO(rabrams) fall back to id if no names
			Name: containers[i].Names[0],
		})
	}
	return response, nil
}

// Mount mounts a volume
func (d ImagefsDriver) Mount(r *volume.MountRequest) (*volume.MountResponse, error) {
	containerID, err := d.FindVolumeContainer(r.Name)
	if err != nil {
		return nil, fmt.Errorf("unexpected error: %s", err)
	}

	err = d.cli.ContainerStart(
		context.Background(),
		containerID,
		types.ContainerStartOptions{},
	)
	if err != nil {
		return nil, fmt.Errorf("unexpected error: %s", err)
	}

	var _ret *volume.MountResponse
	ret, err := d.Path(&volume.PathRequest{Name: r.Name})
	if ret != nil {
		_ret = &volume.MountResponse{
			Mountpoint: ret.Mountpoint,
		}
	}
	return _ret, err
}

// Unmount unmounts a volume
func (d ImagefsDriver) Unmount(r *volume.UnmountRequest) error {
	fmt.Printf("-> Unmount %+v\n", r)
	containerID, err := d.FindVolumeContainer(r.Name)
	if err != nil {
		return fmt.Errorf("unexpected error: %s", err)
	}

	timeout := time.Second * 5
	err = d.cli.ContainerStop(
		context.Background(),
		containerID,
		&timeout,
	)
	if err != nil {
		return fmt.Errorf("unexpected error: %s", err)
	}
	fmt.Printf("<- OK\n")
	return nil
}

// Remove removes a volume
func (d ImagefsDriver) Remove(r *volume.RemoveRequest) error {
	fmt.Printf("-> Get %+v\n", r)
	containerID, err := d.FindVolumeContainer(r.Name)
	container, err := d.cli.ContainerInspect(context.Background(), containerID)
	if err != nil {
		return fmt.Errorf("unexpected error: %s", err)
	}
	/*if container.State.Running { // force remove
		timeout := 5 * time.Second
		err = d.cli.ContainerStop(context.Background(), containerID, &timeout)
		if err != nil {
			return fmt.Errorf("unexpected error: %s", err)
		}
	}*/
	target, ok := container.Config.Labels["com.docker.imagefs.target"]
	if ok {
		_, err := d.cli.ContainerCommit(context.Background(), r.Name, types.ContainerCommitOptions{
			Reference: target,
		})
		if err != nil {
			return fmt.Errorf("unexpected error: %s", err)
		}
		parts := strings.Split(target, "/")
		if len(parts) == 3 {
			// push the image
			readCloser, err := d.cli.ImagePush(context.Background(), target, types.ImagePushOptions{
				// HACK assume the registry ignores the auth header
				RegistryAuth: "null",
			})
			if err != nil {
				return fmt.Errorf("unexpected error: %s", err)
			}
			scanner := bufio.NewScanner(readCloser)
			for scanner.Scan() {
			}
		}
	}
	err = d.cli.ContainerRemove(context.Background(), r.Name, types.ContainerRemoveOptions{
		Force: true,
	})
	if err != nil {
		return fmt.Errorf("unexpected error: %s", err)
	}

	return nil
}

// Path gets the mounted path of a volume
func (d ImagefsDriver) Path(r *volume.PathRequest) (*volume.PathResponse, error) {
	getReq := volume.GetRequest{
		Name: r.Name,
	}
	ret, err := d.Get(&getReq)
	var _ret *volume.PathResponse
	if ret != nil {
		_ret = &volume.PathResponse{
			Mountpoint: ret.Volume.Mountpoint,
		}
	}
	return _ret, err
}

// Capabilities returns the capabilities of the volume driver
func (d ImagefsDriver) Capabilities() *volume.CapabilitiesResponse {
	fmt.Printf("-> Capabilities\n")
	response := volume.CapabilitiesResponse{Capabilities: volume.Capability{Scope: "local"}}
	fmt.Printf("<- %+v\n", response)
	return &response
}
