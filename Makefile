DOCKER_REPO = ${BLIMP_DOCKER_REPO}
REGISTRY_HOSTNAME ?= blimp-registry.kelda.io
LOGIN_PROXY_HOSTNAME ?= blimp-login.kelda.io
# Only needs to be set during local development if the manager is being
# deployed to a remote cluster.
CLUSTER_MANAGER_IP ?= 8.8.8.8
CLUSTER_MANAGER_HOST ?= blimp-manager.kelda.io:443
REGISTRY_IP ?= 8.8.8.8
REGISTRY_STORAGE ?= "5Gi"
#VERSION?=$(shell ./scripts/dev_version.sh)
VERSION?=latest
MANAGER_KEY_PATH = "./certs/cluster-manager.key.pem"
MANAGER_CERT_PATH = "./certs/cluster-manager.crt.pem"
LD_FLAGS = "-X github.com/kelda-inc/blimp/pkg/version.Version=${VERSION} \
	   -X github.com/kelda-inc/blimp/pkg/version.NodeControllerImage=${NODE_CONTROLLER_IMAGE} \
	   -X github.com/kelda-inc/blimp/pkg/version.DNSImage=${DNS_IMAGE} \
	   -X github.com/kelda-inc/blimp/pkg/version.InitImage=${INIT_IMAGE} \
	   -X github.com/kelda-inc/blimp/pkg/version.SyncthingImage=${SYNCTHING_IMAGE} \
	   -X github.com/kelda-inc/blimp/pkg/version.ReservationImage=${RESERVATION_IMAGE} \
	   -X main.LoginProxyHost=${LOGIN_PROXY_HOSTNAME} \
	   -X main.RegistryHostname=${REGISTRY_HOSTNAME} \
	   -s -w"

# Include override variables. The production Makefile takes precendence if it exists.
-include local.mk
-include prod.mk

install:
	CGO_ENABLED=0 go install -ldflags $(LD_FLAGS) ./...

syncthing-macos:
	curl -L -O https://github.com/syncthing/syncthing/releases/download/v1.4.0/syncthing-macos-amd64-v1.4.0.tar.gz
	tar -xf syncthing*.tar.gz
	mv syncthing-macos-amd64-v1.4.0/syncthing syncthing-macos
	rm -rf syncthing-macos-amd64*

syncthing-linux:
	curl -L -O https://github.com/syncthing/syncthing/releases/download/v1.4.0/syncthing-linux-amd64-v1.4.0.tar.gz
	tar -xf syncthing*.tar.gz
	mv syncthing-linux-amd64-v1.4.0/syncthing syncthing-linux
	rm -rf syncthing-linux-amd64*

generate:
	protoc -I $$GOPATH/src/ github.com/kelda-inc/blimp/_proto/blimp/wait/v0/wait.proto --go_out=plugins=grpc:$$GOPATH/src

certs:
	./scripts/make-manager-cert.sh ${MANAGER_CERT_PATH} ${MANAGER_KEY_PATH} ${CLUSTER_MANAGER_IP}

run-cluster-controller: certs
	go run -ldflags $(LD_FLAGS) ./cluster-controller -tls-cert ${MANAGER_CERT_PATH} -tls-key ${MANAGER_KEY_PATH}

build-circle-image:
	docker build -f .circleci/Dockerfile . -t keldaio/circleci-blimp

test:
	go test ./...

NODE_CONTROLLER_IMAGE = ${DOCKER_REPO}/blimp-node-controller:${VERSION}
DNS_IMAGE = ${DOCKER_REPO}/blimp-dns:${VERSION}
CLUSTER_CONTROLLER_IMAGE = ${DOCKER_REPO}/blimp-cluster-controller:${VERSION}
INIT_IMAGE = ${DOCKER_REPO}/blimp-init:${VERSION}
SYNCTHING_IMAGE = ${DOCKER_REPO}/sandbox-syncthing:${VERSION}
DOCKER_AUTH_IMAGE = ${DOCKER_REPO}/blimp-docker-auth:${VERSION}
LOGIN_PROXY_IMAGE = ${DOCKER_REPO}/login-proxy:${VERSION}
RESERVATION_IMAGE = ${DOCKER_REPO}/sandbox-reservation:${VERSION}

build-docker: certs
	# Exit if the base container fails to build.
	docker build -t blimp-go-build --build-arg COMPILE_FLAGS=${LD_FLAGS} .

	docker build -t sandbox-syncthing -t ${SYNCTHING_IMAGE} -f ./sandbox/syncthing/Dockerfile . & \
	docker build -t blimp-cluster-controller -t ${CLUSTER_CONTROLLER_IMAGE} - < ./cluster-controller/Dockerfile & \
	docker build -t blimp-node-controller -t ${NODE_CONTROLLER_IMAGE} - < ./node/Dockerfile & \
	docker build -t blimp-dns -t ${DNS_IMAGE} - < ./sandbox/dns/Dockerfile & \
	docker build -t blimp-init -t ${INIT_IMAGE} - < ./sandbox/init/Dockerfile & \
	docker build -t blimp-docker-auth -t ${DOCKER_AUTH_IMAGE} - < ./registry/Dockerfile & \
	docker build -t login-proxy -t ${LOGIN_PROXY_IMAGE} - < ./login-proxy/Dockerfile & \
	docker build -t sandbox-reservation -t ${RESERVATION_IMAGE} - < ./sandbox/reservation/Dockerfile & \
	wait # Wait for all background jobs to exit before continuing so that we can guarantee the images are built.

push-docker: build-docker
	docker push ${NODE_CONTROLLER_IMAGE} ;
	docker push ${CLUSTER_CONTROLLER_IMAGE} & \
	docker push ${DNS_IMAGE} & \
	docker push ${SYNCTHING_IMAGE} & \
	docker push ${INIT_IMAGE} & \
	docker push ${DOCKER_AUTH_IMAGE} & \
	docker push ${LOGIN_PROXY_IMAGE} & \
	docker push ${RESERVATION_IMAGE} & \
	wait # Wait for all background jobs to exit before continuing so that we can guarantee the images are pushed.

deploy-registry:
	sed -i.bak 's|<DOCKER_AUTH_IMAGE>|${DOCKER_AUTH_IMAGE}|' ./registry/kube/registry-deployment.yaml
	sed -i.bak 's|<REGISTRY_HOSTNAME>|${REGISTRY_HOSTNAME}|' ./registry/kube/registry-deployment.yaml
	sed -i.bak 's|<REGISTRY_IP>|${REGISTRY_IP}|' ./registry/kube/registry-service.yaml
	sed -i.bak 's|<REGISTRY_STORAGE>|${REGISTRY_STORAGE}|' ./registry/kube/registry-pvc.yaml
	kubectl apply -f ./registry/kube

deploy-manager:
	sed -i.bak 's|<CLUSTER_MANAGER_IMAGE>|${CLUSTER_CONTROLLER_IMAGE}|' ./cluster-controller/kube/manager-deployment.yaml
	sed -i.bak 's|<CLUSTER_MANAGER_IP>|${CLUSTER_MANAGER_IP}|' ./cluster-controller/kube/manager-service.yaml
	kubectl apply -f ./cluster-controller/kube

deploy-login-proxy:
	sed -i.bak 's|<LOGIN_PROXY_IMAGE>|${LOGIN_PROXY_IMAGE}|' ./login-proxy/kube/login-deployment.yaml
	sed -i.bak 's|<LOGIN_PROXY_HOSTNAME>|${LOGIN_PROXY_HOSTNAME}|' ./login-proxy/kube/login-deployment.yaml
	sed -i.bak 's|<LOGIN_PROXY_IP>|${LOGIN_PROXY_IP}|' ./login-proxy/kube/login-service.yaml
	kubectl apply -f ./login-proxy/kube

lint:
	golangci-lint run

lint-fix:
	golangci-lint run --fix
