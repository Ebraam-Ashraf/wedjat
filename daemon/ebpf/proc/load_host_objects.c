#include <bpf/libbpf.h>
#include <errno.h>
#include <stdio.h>
#include <string.h>

int main(int argc, char **argv) {
    int i;

    if (argc < 2) {
        fprintf(stderr, "usage: %s object.bpf.o [object.bpf.o ...]\n", argv[0]);
        return 2;
    }
    for (i = 1; i < argc; i++) {
        struct bpf_object *obj;
        int err;

        errno = 0;
        obj = bpf_object__open_file(argv[i], NULL);
        if (!obj) {
            fprintf(stderr, "FAIL open %s: %s\n", argv[i], strerror(errno));
            return 1;
        }
        err = bpf_object__load(obj);
        if (err) {
            fprintf(stderr, "FAIL load %s: %s\n", argv[i], strerror((int)-err));
            bpf_object__close(obj);
            return 1;
        }
        printf("PASS verifier load: %s\n", argv[i]);
        bpf_object__close(obj);
    }
    return 0;
}
