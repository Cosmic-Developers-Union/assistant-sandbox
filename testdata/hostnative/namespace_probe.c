#include <stdio.h>
#include <unistd.h>

int main(void) {
    const char *names[] = {"user", "mnt", "pid", "net", "ipc", "uts", "cgroup"};
    printf("arch=x86_64 pid=%ld\n", (long)getpid());
    for (size_t i = 0; i < sizeof(names) / sizeof(names[0]); ++i) {
        char path[64];
        char target[256];
        snprintf(path, sizeof(path), "/proc/self/ns/%s", names[i]);
        ssize_t length = readlink(path, target, sizeof(target) - 1);
        if (length < 0) {
            perror(path);
            return 1;
        }
        target[length] = '\0';
        printf("%s=%s\n", names[i], target);
    }
    return 0;
}
