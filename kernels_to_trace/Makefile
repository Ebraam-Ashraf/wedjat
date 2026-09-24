BUILD := build

all: $(BUILD) $(patsubst %.cu,$(BUILD)/%,$(wildcard k*.cu))

$(BUILD):
	mkdir -p $@

$(BUILD)/%: %.cu
	nvcc -o $@ $<

clean:
	rm -rf $(BUILD)
