/* P08 探针:PTY 主从、isatty、窗口大小、termios 与管道差异。 */
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <string.h>
#include <sys/ioctl.h>
#include <termios.h>
#include <unistd.h>

int main(void) {
	int pipefd[2];
	if (pipe(pipefd) != 0) return 10;
	if (isatty(pipefd[0]) != 0) return 11;
	if (errno != ENOTTY) return 12;
	int master = open("/dev/ptmx", O_RDWR | O_NOCTTY);
	if (master < 0) return 20;
	if (!isatty(master)) return 21;
	int number = -1;
	if (ioctl(master, TIOCGPTN, &number) != 0 || number != 0) return 22;
	int unlock = 0;
	if (ioctl(master, TIOCSPTLCK, &unlock) != 0) return 23;
	int slave = open("/dev/pts/0", O_RDWR | O_NOCTTY);
	if (slave < 0) return 24;
	if (!isatty(slave)) return 25;
	struct winsize size = {.ws_row = 40, .ws_col = 100};
	if (ioctl(slave, TIOCSWINSZ, &size) != 0) return 26;
	struct winsize got = {0};
	if (ioctl(master, TIOCGWINSZ, &got) != 0) return 27;
	if (got.ws_row != 40 || got.ws_col != 100) return 28;
	struct termios term;
	if (tcgetattr(slave, &term) != 0) return 30;
	term.c_lflag &= ~(ICANON | ECHO);
	if (tcsetattr(slave, TCSANOW, &term) != 0) return 31;
	term.c_lflag |= ICANON;
	term.c_lflag &= ~ECHO;
	if (tcsetattr(slave, TCSANOW, &term) != 0) return 32;
	if (write(master, "hello\n", 6) != 6) return 33;
	char buffer[16];
	int n = 0;
	if ((n = (int)read(slave, buffer, sizeof(buffer))) != 6 || memcmp(buffer, "hello\n", 6) != 0) return 34;
	if (write(slave, "world", 5) != 5) return 35;
	if ((n = (int)read(master, buffer, sizeof(buffer))) != 5 || memcmp(buffer, "world", 5) != 0) return 36;
	if (write(1, "P08-OK\n", 7) != 7) return 37;
	return 0;
}
