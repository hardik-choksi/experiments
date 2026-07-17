/*
 * memwave - generate a wave pattern in memory usage.
 *
 * Ramps RSS up to a peak in steps, holds, then frees back down in steps,
 * repeating forever. Pages are touched after allocation so the memory
 * actually counts as resident (RSS), not just virtual.
 *
 * Usage: memwave [-m gigabytes] [-c cycle_seconds] [-s steps]
 */

#define _GNU_SOURCE
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

static volatile sig_atomic_t stop = 0;

static void on_signal(int sig)
{
    (void)sig;
    stop = 1;
}

static void sleep_ms(long ms)
{
    struct timespec ts = { ms / 1000, (ms % 1000) * 1000000L };
    while (nanosleep(&ts, &ts) != 0 && !stop)
        ;
}

/* Touch every page so the kernel actually backs the allocation. */
static void touch_pages(char *buf, size_t len)
{
    long page = sysconf(_SC_PAGESIZE);
    for (size_t i = 0; i < len; i += (size_t)page)
        buf[i] = 1;
}

static void usage(const char *prog)
{
    fprintf(stderr,
            "usage: %s [-m gigabytes] [-c cycle_seconds] [-s steps]\n"
            "  -m  peak memory in GB (default 2)\n"
            "  -c  full cycle duration in seconds (default 60)\n"
            "  -s  allocation steps per ramp (default 20)\n",
            prog);
    exit(1);
}

int main(int argc, char **argv)
{
    double peak_gb = 2.0;
    long cycle_s = 60;
    int steps = 20;
    int opt;

    while ((opt = getopt(argc, argv, "m:c:s:h")) != -1) {
        switch (opt) {
        case 'm':
            peak_gb = atof(optarg);
            break;
        case 'c':
            cycle_s = atol(optarg);
            break;
        case 's':
            steps = atoi(optarg);
            break;
        default:
            usage(argv[0]);
        }
    }
    if (peak_gb <= 0 || cycle_s <= 0 || steps <= 0)
        usage(argv[0]);

    size_t peak_bytes = (size_t)(peak_gb * 1024 * 1024 * 1024);
    size_t chunk = peak_bytes / (size_t)steps;

    /* Cycle budget: 40% ramp up, 10% hold at peak, 40% ramp down, 10% idle. */
    long ramp_ms = cycle_s * 1000 * 4 / 10;
    long hold_ms = cycle_s * 1000 / 10;
    long step_ms = ramp_ms / steps;

    char **chunks = calloc((size_t)steps, sizeof(char *));
    if (!chunks) {
        perror("calloc");
        return 1;
    }

    signal(SIGINT, on_signal);
    signal(SIGTERM, on_signal);

    printf("memwave: peak %.2f GB, cycle %lds, %d steps of %.1f MB\n",
           peak_gb, cycle_s, steps, (double)chunk / (1024 * 1024));

    unsigned long cycle = 0;
    while (!stop) {
        cycle++;
        printf("cycle %lu: ramping up\n", cycle);
        for (int i = 0; i < steps && !stop; i++) {
            chunks[i] = malloc(chunk);
            if (!chunks[i]) {
                fprintf(stderr, "malloc failed at step %d, freeing\n", i);
                break;
            }
            touch_pages(chunks[i], chunk);
            sleep_ms(step_ms);
        }

        if (!stop) {
            printf("cycle %lu: holding at peak\n", cycle);
            sleep_ms(hold_ms);
        }

        printf("cycle %lu: ramping down\n", cycle);
        for (int i = steps - 1; i >= 0; i--) {
            free(chunks[i]);
            chunks[i] = NULL;
            if (!stop)
                sleep_ms(step_ms);
        }

        if (!stop) {
            printf("cycle %lu: idle\n", cycle);
            sleep_ms(hold_ms);
        }
    }

    free(chunks);
    printf("memwave: stopped after %lu cycle(s)\n", cycle);
    return 0;
}
