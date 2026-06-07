MODULE_DIRS := agent-cli go-agent-loop go-llm-gateway

.PHONY: deps typecheck lint test validate

deps:
	@for dir in $(MODULE_DIRS); do \
		$(MAKE) -C $$dir deps || exit $$?; \
	done

typecheck:
	@for dir in $(MODULE_DIRS); do \
		$(MAKE) -C $$dir build || exit $$?; \
	done

lint:
	@for dir in $(MODULE_DIRS); do \
		$(MAKE) -C $$dir vet || exit $$?; \
	done

test:
	@for dir in $(MODULE_DIRS); do \
		$(MAKE) -C $$dir test || exit $$?; \
	done

validate: typecheck lint test
