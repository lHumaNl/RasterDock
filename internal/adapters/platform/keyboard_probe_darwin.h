//go:build integration && darwin

#include <stdint.h>
int rd_probe_permissions(void);
uint64_t rd_probe_identity(int pid, unsigned int window, const char *title);
int rd_probe_check(int pid, unsigned int window, const char *title, uint64_t identity);
int rd_probe_key(int pid, unsigned int window, const char *title, uint64_t identity, unsigned short key);
int rd_probe_viewer_focus(const char *title);
