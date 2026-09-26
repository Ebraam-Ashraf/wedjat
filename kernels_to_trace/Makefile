BUILD := build

.PHONY: all build clean

# 'build' is a phony alias for 'all'.
# Do NOT list $(BUILD) as a prerequisite here — that would clash with .PHONY: build
# since $(BUILD) expands to 'build', causing a circular dependency.
# The mkdir is handled inline in the pattern rule below instead.
all: $(patsubst %.cu,$(BUILD)/%,$(wildcard k*.cu))
build: all

$(BUILD)/%: %.cu
	@mkdir -p $(BUILD)
	nvcc -arch=native -o $@ $<

clean:
	rm -rf $(BUILD)
