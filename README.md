[![Build Status](https://travis-ci.com/jbrekelmans/kube-compose.svg?branch=master)](https://travis-ci.com/jbrekelmans/kube-compose)
[![License](https://img.shields.io/badge/license-Apache_v2.0-blue.svg)](https://github.com/jbrekelmans/kube-compose/blob/master/LICENSE.md)
[![Coverage Status](https://coveralls.io/repos/github/jbrekelmans/kube-compose/badge.svg?branch=master&r=2)](https://coveralls.io/github/jbrekelmans/kube-compose?branch=master?r=2)

# Introduction

kube-compose is a CI tool that can create and destroy environments in Kubernetes based on docker compose files.

## Contents

* [Installation](#Installation)
* [Getting Started](#Getting-Started)
  * [Build And Package](#Build-And-Package)
  * [Running Tests](#Running-Tests)
* [Commands](#Commands)
* [Examples](#Examples)
* [Advanced Usage](#Advanced-Usage)

## Installation

Use the following to be able to install on MacOS via Homebrew:

Running the below command will add the Homebrew tap to our repository

```bash
brew tap kube-compose/homebrew-kube-compose
```

Now you've added our custom tap, you can download with the following command:

```bash
brew install kube-compose
```

To upgrade kube-compose to the latest stable release use the following command:

```bash
brew upgrade kube-compose
```

Otherwise download the binary from https://github.com/jbrekelmans/kube-compose/releases, and place it on your `PATH`.

## Getting Started

### Build And Package

You can compile the kube-compose binary using either Go or Docker-compose.

Using Go:

```go
go build .
```

Using Makefile (**Recommended**):

```make
make releases
```

*Note: Will build for Linux, MacOS (darwin), and Windows.*

### Testing

Use `kubectl` to set the target Kubernetes namespace and the service account of kube-compose.

Run `kube-compose` with the test [docker-compose.yml](test/docker-compose.yml):

```bash
kube-compose -f test/docker-compose.yml --env-id test123 up
```

This writes the created Kubernetes resources to the directory test/output.

To clean up after the test:

```bash
kube-compose down
```

## Commands

The following is a list of all available commands:

```bash
Available Commands:
  up          Create and start containers running on K8s
  down        Stop and remove containers, networks, images, and volumes running on K8s
  help        Help about any command
```

The following is a list of global flags:

```bash
 -e, --env-id string      used to isolate environments deployed to a shared namespace, by (1) using this value as a suffix of pod and service names and (2) using this value to isolate selectors. Either this flag or the environment variable KUBECOMPOSE_ENVID must be set
  -f, --file string        Specify an alternate compose file
  -n, --namespace string   namespace for environment
  -h, --help               help for kube-compose
```

kube-compose currently supports environment variables for certain flags. If these environment variables are set, you don't need to pass the `--namespace` and `--env-id` flags.

```bash
export KUBECOMPOSE_NAMESPACE=""
export KUBECOMPOSE_ENVID=""
```

Intuitively, the `kube-compose up` mirrors functionality of `docker-compose up`, but runs containers on a Kubernetes cluster instead of on the host docker. Likewise `kube-compose down` behaves in a similar fashion.

## Examples

To create pods and services in K8s from a docker-compose file run the following command:

```bash
kube-compose -e [build-id] up
```

The created resources names will be suffixed with build-id and their selectors will include env: build-id.

The target namespace and service account token are loaded from the context set in `~/.kube/config`. This means that k8s Client tool kubectl commands can be used to configure kube-compose's target namespace and service account.

If no `~/.kube/config` exists and kube-compose is run inside a pod in Kubernetes, the pod's namespace becomes the target namespace, and the service account used to create pods and services is the pod's service account.

To read more about how the ~/.kube/config file works read the documentation [here](https://kubernetes.io/docs/concepts/configuration/organize-cluster-access-kubeconfig/).

The namespace can be overridden via the `--namespace` option, for example: `kube-compose --namespace ci up`.

### Foreground mode to view the logs of running pods

```bash
kube-compose --namespace default --env-id test123 up

kube-compose --namespace default --env-id test123 down
```

```bash
kube-compose up -n default -e test123

kube-compose down -n default -e test123

```

If environment variables are already set.

```bash
kube-compose up

kube-compose down
```

Start individual services defined in docker-compose.yml

```bash
kube-compose up service-1

kube-compose up service-1 service-2
```

### Detached mode

```bash
kube-compose --namespace default --env-id test123 up --detach
```

```bash
kube-compose up -n default -e test123 -d
```

If environment variables are already set.

```bash
kube-compose up -d
```

## Why another tool

Although [kompose](https://github.com/kubernetes/kompose) can already convert docker compose files into Kubernetes resources, the main differences between kube-compose and Kompose are:

1. kube-compose generates Kubernetes resource names and selectors that are unique for each build to support shared namespaces and scaling to many concurrent CI environments.

1. kube-compose creates pods with `restartPolicy: Never` instead of deployments, so that failed pods can be inspected, no logs are lost due to pod restarts, and Kubernetes cluster resources are used more efficiently.

1. kube-compose allows startup dependencies to be specified by respecting [docker compose](https://docs.docker.com/compose/compose-file/compose-file-v2#depends_on)'s `depends_on` field.

1. kube-compose currently depends on the docker daemon to pull Docker images and extract their healthcheck.

## Advanced Usage

### Dependency healthchecks

If you require that an application is not started until one of its dependencies is healthy, you can add `condition: service_healthy` to the `depends_on`, and give the dependency a [Docker healthcheck](https://docs.docker.com/engine/reference/builder#healthcheck).

Docker healthchecks are converted into [Readiness Probes](https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-probes/).

### Running as specific users

If any of the images you are running with kube-compose need to be run as a specific user, you can set the `--run-as-user` flag for `kube-compose up`. This will set each pod's `runAsUser`/`runAsGroup` based on either the [`user` property](https://docs.docker.com/compose/compose-file/#domainname-hostname-ipc-mac_address-privileged-read_only-shm_size-stdin_open-tty-user-working_dir) of its docker-compose service or the [`USER` configuration](https://docs.docker.com/engine/reference/builder/#user) of its Docker image (prioritising the former if both are present).

Bear in mind that if a Dockerfile does not explicitly include the `USER` instruction, it will recursively follow its base image (as defined in the `FROM` instruction) until it finds an image that did configure its `USER`. This could result in vulnerabilities, such as accidentally running as root.
