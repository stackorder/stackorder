TF ?= terraform
DIRS := $(shell find modules stacks infra -name '*.tf' -not -path '*/.terraform/*' -exec dirname {} \; | sort -u)
TEST_DIRS := $(sort $(patsubst %/tests,%,$(patsubst %/,%,$(dir $(shell find modules stacks infra -name '*.tftest.hcl' -not -path '*/.terraform/*')))))

.PHONY: fmt validate test graph

fmt:
	$(TF) fmt -recursive

validate:
	@set -e; for dir in $(DIRS); do \
		echo "==> $$dir"; \
		$(TF) -chdir=$$dir init -backend=false -input=false -no-color >/dev/null; \
		$(TF) -chdir=$$dir validate -no-color; \
	done

test:
	@set -e; for dir in $(TEST_DIRS); do \
		echo "==> $$dir"; \
		$(TF) -chdir=$$dir init -backend=false -input=false -no-color >/dev/null; \
		$(TF) -chdir=$$dir test -no-color; \
	done

graph:
	@if command -v stackorder >/dev/null 2>&1; then \
		stackorder graph --format dot; \
	else \
		echo "stackorder is not on PATH; install it from https://github.com/stackorder/stackorder/releases"; \
	fi
