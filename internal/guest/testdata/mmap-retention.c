/* 回归:文件映射在 fd 关闭后仍然有效(Linux mmap 语义)。
 * sem_open 会在 mmap 后关闭 /dev/shm 文件描述符;随后的 sem_wait
 * 必须在映射内存上完成原子操作,不能因句柄关闭而 EFAULT/EBADF。 */
#include <errno.h>
#include <fcntl.h>
#include <semaphore.h>
#include <stdio.h>
#include <string.h>

int main(void) {
    sem_unlink("/mmap-retention");
    sem_t *sem = sem_open("/mmap-retention", O_CREAT | O_EXCL, 0600, 1);
    if (sem == SEM_FAILED) { printf("open errno=%d %s\n", errno, strerror(errno)); return 1; }
    if (sem_wait(sem) != 0) { printf("wait errno=%d\n", errno); return 2; }
    if (sem_post(sem) != 0) { printf("post errno=%d\n", errno); return 3; }
    if (sem_close(sem) != 0) { printf("close errno=%d\n", errno); return 4; }
    if (sem_unlink("/mmap-retention") != 0) { printf("unlink errno=%d\n", errno); return 5; }
    printf("MMAP-RETENTION-OK\n");
    return 0;
}
