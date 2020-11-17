PLATFORM=local

REPOSITORY=yoannma/scaleway_exporter
VERSION=0.1.0

export DOCKER_BUILDKIT=1
export COMPOSE_DOCKER_CLI_BUILD=1

.PHONY: docker
docker: build-image push-image

.PHONY: build-image
build-image:
	@docker build . -t ${REPOSITORY}:${VERSION} \
	--target bin \
	--platform ${PLATFORM}

.PHONY: push-image
push-image:
	@docker push ${REPOSITORY}:${VERSION}

.PHONY: bin/scaleway_exporter
bin/scaleway_exporter:
	@docker build . --target bin \
	--output bin/ \
	--platform ${PLATFORM}

.PHONY: lint
lint:
	@docker build . --target lint

.PHONY: compose-build
compose:
	@docker-compose build

.PHONY: compose-up
compose:
	@docker-compose up -d

.PHONY: compose
compose: compose-build compose-up