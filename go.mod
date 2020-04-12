module github.com/kelda-inc/blimp

go 1.13

// Required to build github.com/moby/buildkit:
// * https://github.com/golang/go/issues/35787
// * https://github.com/containerd/containerd/issues/3031#issuecomment-541737892
replace (
	github.com/containerd/containerd v1.3.0-0.20190507210959-7c1e88399ec0 => github.com/containerd/containerd v1.3.0-beta.2.0.20190823190603-4a2f61c4f2b4
	github.com/docker/docker => github.com/moby/moby v0.7.3-0.20190826074503-38ab9da00309
	golang.org/x/crypto v0.0.0-20190129210102-0709b304e793 => golang.org/x/crypto v0.0.0-20180904163835-0709b304e793
)

require (
	github.com/Azure/go-ansiterm v0.0.0-20170929234023-d6e3b3328b78 // indirect
	github.com/GeertJohan/go.rice v1.0.0
	github.com/Masterminds/semver v1.5.0
	github.com/Microsoft/hcsshim v0.8.7 // indirect
	github.com/buger/goterm v0.0.0-20200322175922-2f3e71b85129
	github.com/cesanta/docker_auth/auth_server v0.0.0-20200309093330-99bfe0217f59
	github.com/compose-spec/compose-go v0.0.0-20200325144548-3eee68c9fd46
	github.com/containerd/continuity v0.0.0-20200228182428-0f16d7a0959c // indirect
	github.com/coreos/go-oidc v2.2.1+incompatible
	github.com/daaku/go.zipexe v1.0.1 // indirect
	github.com/docker/cli v0.0.0-20200320120634-22acbbcc4b3f
	github.com/docker/docker v1.14.0-0.20190319215453-e7b5f7dbe98c
	github.com/docker/docker-credential-helpers v0.6.3 // indirect

	// At the moment, the latest release doesn't contain the yaml.UnmarshalStrict
	// and yaml.DisallowUnknownFields functions. This override vendors the latest
	// commit on `master`.
	github.com/ghodss/yaml v1.0.1-0.20190212211648-25d852aebe32
	github.com/golang/protobuf v1.3.3
	github.com/googleapis/gnostic v0.4.0 // indirect
	github.com/gorilla/mux v1.7.4 // indirect
	github.com/kr/pretty v0.2.0 // indirect
	github.com/miekg/dns v1.1.28
	github.com/mitchellh/go-homedir v1.1.0
	github.com/morikuni/aec v0.0.0-20170113033406-39771216ff4c // indirect
	github.com/opencontainers/image-spec v1.0.1 // indirect
	github.com/opencontainers/runc v0.1.1 // indirect
	github.com/pquerna/cachecontrol v0.0.0-20180517163645-1555304b9b35 // indirect
	github.com/sirupsen/logrus v1.4.2
	github.com/spf13/cobra v0.0.6
	github.com/stretchr/testify v1.4.0
	github.com/syncthing/syncthing v1.4.2
	golang.org/x/crypto v0.0.0-20191011191535-87dc89f01550
	golang.org/x/oauth2 v0.0.0-20200107190931-bf48bf16ab8d
	google.golang.org/grpc v1.28.0
	gopkg.in/check.v1 v1.0.0-20190902080502-41f04d3bba15 // indirect
	gopkg.in/square/go-jose.v2 v2.4.1 // indirect
	k8s.io/api v0.17.3
	k8s.io/apimachinery v0.17.3
	k8s.io/client-go v0.17.3
	k8s.io/utils v0.0.0-20200229041039-0a110f9eb7ab // indirect
)
