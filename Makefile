IMAGE ?= ghcr.io/blackdark/csi-driver-mergerfs:0.1.1
GO_MOD_DIR := mergerfs-csi
CHART := charts/mergerfs-csi

.PHONY: build test docker-build helm-lint

build:
	cd $(GO_MOD_DIR) && go build -o ../bin/mergerfs-csi ./cmd/mergerfs-csi

test:
	cd $(GO_MOD_DIR) && go test ./...

docker-build:
	docker build --platform=linux/amd64 -t $(IMAGE) .

helm-lint:
	helm lint $(CHART)
