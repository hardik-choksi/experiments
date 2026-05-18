# Libraries

This document explains how to create shared and static libraries in c.

As you can see in directory, we are creating an example library `libstr`, which contains function to reverse a string.

We will create a header file and its implementation file. (`libstr.h` and `libstr.c`)

We will compile this as shared and static both.

Makefile contains all make commands to compile libraries, and link them with programs to generate an executable.

## Shared Library

### Making Shared Library

Shared library files end with `.so` extension.
You can make a shared library with following command.

```bash
gcc -Wall -g -fPIC -shared -o libstr.so libstr.c -lc
```

Here, meaning of flags:

- **`fPIC`**:
  position independent code, code that can be placed anywhere in memory and can be accessed correclty.
- **`shared`**: makes lib shared.

### Making shared library available across system

There are some default directories where compiler looks for shared libraries. You can find those for linux using following command:

```bash
ld --verbose | grep SEARCH_DIR | tr -s ' ;' \\012
```

some common directories for shared libraries:

- `/lib`
- `/usr/lib`
- `/usr/local/lib`

### Using shared lib

You have to compile your code bit differently in order to use shared lib.

```bash
gcc -Wall -g -o runtime_libtest libtest.c -L. -lstr
```

- **`-L`**: Look for the library in the current directory.
- **`-lstr`**: Tells compiler to link program with `libstr.so`. Compiler assumes that libraries will start from `lib` so `l` stands for `lib`.

## Static Library

extension is .a
they're made with `ar` command. stands for archive

### Making static lib

```bash
ar rcs libstaticstr.a libstr.o
```

- **`r`(replace)**: replace file with same name in archive
- **`c`(create)**: create archive
- **`s`(index)**: generates an index, used by compiler to make sense of lib

### Using static lib

After generating `.a` file using `ar`, use following command to link static library with program.

```bash
gcc -Wall -g -o static_libtest libtest.c -L. -lstaticstr
```

## References
- Jacob Sorber https://youtu.be/JbHmin2Wtmc?si=betLXy7geomfcqHs
- https://www.youtube.com/playlist?list=PLIz6U0slZNq2TS1zSUjZHgxBjAJL4nb92
