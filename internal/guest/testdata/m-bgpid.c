/* P13 后台作业探针:把自身 PID 写到 /work/pid,然后阻塞等待信号。 */
#include <stdio.h>
#include <unistd.h>

int main(void) {
	FILE *file = fopen("/work/pid", "w");
	if (file == NULL) return 10;
	fprintf(file, "%d\n", (int)getpid());
	fclose(file);
	for (;;) pause();
}
