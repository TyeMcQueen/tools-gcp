SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
# Mac's gnu Make 3.81 does not support .ONESHELL
# .ONESHELL:
.DELETE_ON_ERROR:
MAKEFLAGS += --warn-undefined-variables
MAKEFLAGS += --no-builtin-rules

# COLORS
GREEN  := $(shell tput -Txterm setaf 2)
YELLOW := $(shell tput -Txterm setaf 3)
RESET  := $(shell tput -Txterm sgr0)

.PHONY: help
## Explain available make targets
help:
	@echo ''
	@echo 'Usage:'
	@echo '  ${YELLOW}make${RESET} ${GREEN}<target>${RESET}'
	@echo ''
	@echo 'Targets:'
	@awk '/^[a-zA-Z\-\_\/0-9]+:/ { \
		helpMessage = match(lastLine, /^## (.*)/); \
		if (helpMessage) { \
			helpCommand = substr($$1, 0, index($$1, ":")-1); \
			helpMessage = substr(lastLine, RSTART + 3, RLENGTH); \
			odd = substr(" ", 0, 1-length(odd)); \
			printf "  ${YELLOW}%-15s ${GREEN}%s${RESET}%s\n", \
				helpCommand, helpMessage, odd; \
		} \
	} \
	{ lastLine = $$0 }' $(MAKEFILE_LIST) \
		| sed -e '/[^ ]$$/s/   / . /g' -e 's/ $$//'

.PHONY: prune
## Prune docker images
prune:
	yes | docker system prune

.PHONY: patch
## Increment version patch level then rebuild local containers
patch:
	version-bump patch
	bin/build local gcp2prom

.PHONY: minor
## Increment minor version number then rebuild local containers
minor:
	version-bump minor
	bin/build local gcp2prom

.PHONY: major
## Increment major version number then rebuild local containers
major:
	version-bump major
	bin/build local gcp2prom

.PHONY: rebuild
## Rebuild local containers (leave version number unchanged).
rebuild:
	bin/build local gcp2prom

.PHONY: push
## Push local containers to Docker Hub
push:
	bin/build push gcp2prom

MOD := github.com/TyeMcQueen/tools-gcp/

cover: go.mod */*.go results/project.txt
	GCP_PROJECT_ID=`cat results/project.txt` go test -coverprofile cover ./...

cover.html: cover
	go tool cover -html cover -o cover.html

cover.txt: cover.html
	@grep '<option value=' cover.html | sed \
		-e 's:^.*<option value="[^"][^"]*">${MOD}::' -e 's:</option>.*::' \
		-e 's:^\([^ ][^ ]*\) [(]\(.*\)[)]:\2 \1:' | col-align - > cover.txt
	@echo ''

.PHONY: test
## Run unit tests and report statement coverage percentages
test: cover.txt
	@cat cover.txt

.PHONY: coverage
## View coverage details in your browser
coverage: cover.html
	open cover.html
