# nyamisty/imagefs

**POC ONLY**

Volume plugin to mount images into containers


## Usage

Install the plugin

```
docker plugin install nyamisty/imagefs
docker plugin enable nyamisty/imagefs
```

Create a task with a new volume

```
docker run --mount type=volume,volume-driver=nyamisty/imagefs,volume-opt=source=[src_image_id],target=/context -it alpine /bin/sh
```

* `volume-opt=source` is the image to mount
* `target` is the directory in the container where the image will be mounted

## Limitations

* Does not pull layers that are not present locally in the node
* Only supports overlay2 graphdriver

## Build

```
make plugin
make plugin-push
```