BUILD_DIR=rootfs
BINARY=$(BUILD_DIR)/docker-volume-imagefs
LOOP_BINARY=$(BUILD_DIR)/loop
REPONAME=nyamisty/imagefs

test:
	docker run --rm -v $(CURDIR):/go/src/app -w /go/src/app golang:1.15 sh -c "go get -v && go test -v"

$(BINARY): driver.go main.go
	docker run --rm -v $(CURDIR):/go/src/app -w /go/src/app golang:1.15 sh -c "CGO_ENABLED=0 GOOS=linux go build -ldflags '-s' -a -o $(BINARY); go build -o $(LOOP_BINARY) cmd/loop.go"

binary: $(BINARY)

clean:
	rm -fr $(BUILD_DIR)

image: binary
	docker build -f Dockerfile -t $(REPONAME) .

plugin: binary
	docker plugin rm -f $(REPONAME) || true
	docker plugin create $(REPONAME) .
	docker plugin enable $(REPONAME)

plugin-push: plugin
	docker plugin push $(REPONAME)

image-push:
	docker push $(REPONAME)

deploy:
	docker service create \
		--name imagefs \
		--mode global \
		--mount type=bind,source=/var/run/docker.sock,destination=/var/run/docker.sock \
		--mount type=bind,source=/var/run/docker/plugins,destination=/run/docker/plugins \
		--mount type=bind,source=/var/lib/docker,destination=/var/lib/docker \
		fermayo/imagefs
