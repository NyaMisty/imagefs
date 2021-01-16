package main

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-plugins-helpers/volume"
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
	readCloser, err := d.cli.ImagePull(context.Background(), source, types.ImagePullOptions{
		// HACK assume the registry ignores the auth header
		RegistryAuth: "null",
	})
	if err != nil {
		return fmt.Errorf("unexpected error: %s", err)
	}
	scanner := bufio.NewScanner(readCloser)
	for scanner.Scan() {
	}

	containerConfig := &container.Config{
		Image:      source,
		Entrypoint: []string{"/runtime/loop"},
		Labels: map[string]string{
			"com.docker.imagefs.version": version,
			"com.docker.imagefs.target":  r.Options["target"],
		},
	}
	// TODO handle error
	hostConfig := &container.HostConfig{
		Binds: []string{"/tmp/runtime:/runtime"},
	}
	networkConfig := &network.NetworkingConfig{}
	cont, err := d.cli.ContainerCreate(
		context.Background(),
		containerConfig,
		hostConfig,
		networkConfig,
		nil,
		// TODO(rabrams) namespace
		r.Name,
	)
	if err != nil {
		return fmt.Errorf("unexpected error: %s", err)
	}
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

// List lists available volumes
func (d ImagefsDriver) List() (*volume.ListResponse, error) {
	containers, err := d.cli.ContainerList(context.Background(), types.ContainerListOptions{})
	if err != nil {
		return nil, fmt.Errorf("unexpected error: %s", err)
	}
	response := &volume.ListResponse{}
	for i := range containers {
		_, ok := containers[i].Labels["com.docker.imagefs.version"]
		if !ok {
			continue
		}
		response.Volumes = append(response.Volumes, &volume.Volume{
			// TODO(rabrams) fall back to id if no names
			Name: containers[i].Names[0],
		})
	}
	return response, nil
}

// Get gets a volume
func (d ImagefsDriver) Get(r *volume.GetRequest) (*volume.GetResponse, error) {
	fmt.Printf("-> Mount %+v\n", r)
	container, err := d.cli.ContainerInspect(context.Background(), r.Name)
	if err != nil {
		return nil, fmt.Errorf("unexpected error: %s", err)
	}
	if container.GraphDriver.Name != "overlay" {
		return nil, fmt.Errorf("unexpected graph driver: %s", container.GraphDriver.Name)
	}
	mergedDir, ok := container.GraphDriver.Data["MergedDir"]
	if !ok {
		return nil, fmt.Errorf("missing MergedDir")
	}
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

// Remove removes a volume
func (d ImagefsDriver) Remove(r *volume.RemoveRequest) error {
	timeout := 60 * time.Second
	err := d.cli.ContainerStop(context.Background(), r.Name, &timeout)
	if err != nil {
		return fmt.Errorf("unexpected error: %s", err)
	}
	container, err := d.cli.ContainerInspect(context.Background(), r.Name)
	if err != nil {
		return fmt.Errorf("unexpected error: %s", err)
	}
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

	// HACK remove duplicate container until we can figure out why it is being created
	containers, err := d.cli.ContainerList(context.Background(), types.ContainerListOptions{})
	if err == nil {
		for i := range containers {
			otherTarget, ok := containers[i].Labels["com.docker.imagefs.target"]
			if ok {
				if target == otherTarget {
					d.cli.ContainerRemove(context.Background(), containers[i].ID, types.ContainerRemoveOptions{
						Force: true,
					})
				}
			}
		}
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

// Mount mounts a volume
func (d ImagefsDriver) Mount(r *volume.MountRequest) (*volume.MountResponse, error) {
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
	fmt.Printf("<- OK\n")
	return nil
}

// Capabilities returns the capabilities of the volume driver
func (d ImagefsDriver) Capabilities() *volume.CapabilitiesResponse {
	fmt.Printf("-> Capabilities\n")
	response := volume.CapabilitiesResponse{Capabilities: volume.Capability{Scope: "local"}}
	fmt.Printf("<- %+v\n", response)
	return &response
}
