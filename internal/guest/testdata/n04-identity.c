/* N04 探针:客体通过 libc 查询 passwd/group、环境与 UTF-8 locale。
 * 构建:zig cc -static -target riscv64-linux-musl -O2 -s \
 *   -o n04-identity n04-identity.c
 * 返回码定位首个失败点,成功后输出 N04-OK。 */
#define _GNU_SOURCE
#include <grp.h>
#include <locale.h>
#include <pwd.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <wchar.h>

int main(void) {
	struct passwd *pw = getpwuid(2000);
	if (!pw) return 10;
	if (strcmp(pw->pw_name, "bob") != 0) return 11;
	if (strcmp(pw->pw_dir, "/home/bob") != 0) return 12;
	struct group *gr = getgrgid(2001);
	if (!gr) return 13;
	if (strcmp(gr->gr_name, "bob") != 0) return 14;
	const char *home = getenv("HOME");
	if (!home || strcmp(home, "/home/bob") != 0) return 15;
	const char *path = getenv("PATH");
	if (!path || strcmp(path, "/usr/local/bin:/usr/bin:/bin") != 0) return 16;
	const char *lang = getenv("LANG");
	if (!lang || strcmp(lang, "C.UTF-8") != 0) return 17;
	if (!setlocale(LC_ALL, "")) return 18;
	mbstate_t state = {0};
	wchar_t rune = 0;
	if (mbrtowc(&rune, "\xe4\xbd\xa0", 3, &state) != 3) return 19;
	if (rune != 0x4f60) return 20;
	printf("N04-OK uid=%d gid=%d home=%s lang=%s\n", (int)pw->pw_uid,
	       (int)gr->gr_gid, pw->pw_dir, lang);
	return 0;
}
