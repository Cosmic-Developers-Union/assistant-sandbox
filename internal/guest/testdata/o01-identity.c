/* O01 探针:验证客体 UID/GID、setuid/setgid 权限、文件检查与
 * fork 子进程继承。
 * 断言码:10 初始非 root、11 建目录、12 建 secret、20 setuid、
 * 21 getuid、22 文件检查、23 建文件、24 stat、25 归属、
 * 26 非特权 setuid(0)、30 fork、31 子身份、32 子写文件、
 * 33 waitpid、34 子退出。成功输出 O01-OK。 */
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <unistd.h>

int main(void) {
	if (getuid() != 0) return 10;
	if (mkdir("/work/owned", 0777) != 0 && errno != EEXIST) return 11;
	int secret = open("/work/secret", O_CREAT | O_WRONLY, 0600);
	if (secret < 0) return 12;
	close(secret);
	if (setgid(1234) != 0) return 13;
	if (getgid() != 1234 || getegid() != 1234) return 14;
	if (setuid(1234) != 0) return 20;
	if (getuid() != 1234 || geteuid() != 1234) return 21;
	if (open("/work/secret", O_RDONLY) != -1 || errno != EACCES) return 22;
	int file = open("/work/owned/file", O_CREAT | O_WRONLY, 0644);
	if (file < 0) return 23;
	if (write(file, "x", 1) != 1) return 23;
	close(file);
	struct stat info;
	if (stat("/work/owned/file", &info) != 0) return 24;
	if (info.st_uid != 1234 || info.st_gid != 1234) return 25;
	if (setuid(0) != -1 || errno != EPERM) return 26;
	pid_t child = fork();
	if (child == 0) {
		if (getuid() != 1234 || getgid() != 1234) _exit(31);
		int inherited = open("/work/owned/child", O_CREAT | O_WRONLY, 0644);
		if (inherited < 0) _exit(32);
		if (write(inherited, "y", 1) != 1) _exit(32);
		close(inherited);
		_exit(0);
	}
	if (child < 0) return 30;
	int status = 0;
	if (waitpid(child, &status, 0) < 0) return 33;
	if (!WIFEXITED(status) || WEXITSTATUS(status) != 0) return 34;
	if (write(1, "O01-OK\n", 7) != 7) return 35;
	return 0;
}
