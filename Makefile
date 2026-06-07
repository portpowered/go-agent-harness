.PHONY: test

test:
	$(MAKE) -C go-agent-loop test
	$(MAKE) -C go-llm-gateway test
	$(MAKE) -C agent-cli test
