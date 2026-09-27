INTEGRATION_IMAGE ?= awareof-integration:local

.PHONY: test integration

test:
	go test ./...

integration:
	docker build --file test/integration/Dockerfile --tag $(INTEGRATION_IMAGE) .
	docker run --rm \
		--network none \
		--read-only \
		--user 65532:65532 \
		--cap-drop ALL \
		--security-opt no-new-privileges \
		--pids-limit 256 \
		--memory 1g \
		--tmpfs /tmp:rw,nosuid,exec,size=768m \
		$(INTEGRATION_IMAGE)
