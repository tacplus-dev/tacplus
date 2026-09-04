// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
)

// writeFilesystemHelpers writes directoryToFileSystem and directoryFromFileSystem
// static methods into the Java Helpers class body for the filesystem package.
func writeFilesystemHelpers(b *strings.Builder, outerClassName string) {
	b.WriteString("// ============ Filesystem Helpers ============\n\n")

	b.WriteString(`  private static final java.util.Set<String> SKIP_NAMES = java.util.Set.of(
      ".git", ".svn", ".hg", ".DS_Store");

  private static boolean shouldSkipEntry(java.nio.file.Path entry) throws java.io.IOException {
    String name = entry.getFileName().toString();
    if (SKIP_NAMES.contains(name)) {
      return true;
    }
    // Skip macOS AppleDouble resource-fork sidecar files (e.g. "._main.rego").
    if (name.startsWith("._")) {
      return true;
    }
    // Skip non-regular, non-directory entries (symlinks, sockets, pipes, devices)
    if (!java.nio.file.Files.isRegularFile(entry) && !java.nio.file.Files.isDirectory(entry)) {
      return true;
    }
    return false;
  }

`)

	b.WriteString("  private static " + outerClassName + ".Permission modeToPermission(int mode) {\n")
	b.WriteString("    int m = mode & 0777;\n")
	b.WriteString("    return " + outerClassName + ".Permission.newBuilder()\n")
	b.WriteString("        .setOwner((m >> 6) & 7)\n")
	b.WriteString("        .setGroup((m >> 3) & 7)\n")
	b.WriteString("        .setOther(m & 7)\n")
	b.WriteString("        .build();\n")
	b.WriteString("  }\n\n")

	b.WriteString("  private static int permissionToMode(" + outerClassName + ".Permission p) {\n")
	b.WriteString("    if (p == null || p.equals(" + outerClassName + ".Permission.getDefaultInstance())) {\n")
	b.WriteString("      return 0755;\n")
	b.WriteString("    }\n")
	b.WriteString("    return (p.getOwner() << 6) | (p.getGroup() << 3) | p.getOther();\n")
	b.WriteString("  }\n\n")

	b.WriteString(`  private static java.util.Set<java.nio.file.attribute.PosixFilePermission> modeToPosix(int mode) {
    java.util.Set<java.nio.file.attribute.PosixFilePermission> perms = java.util.EnumSet.noneOf(java.nio.file.attribute.PosixFilePermission.class);
    if ((mode & 0400) != 0) perms.add(java.nio.file.attribute.PosixFilePermission.OWNER_READ);
    if ((mode & 0200) != 0) perms.add(java.nio.file.attribute.PosixFilePermission.OWNER_WRITE);
    if ((mode & 0100) != 0) perms.add(java.nio.file.attribute.PosixFilePermission.OWNER_EXECUTE);
    if ((mode & 040) != 0) perms.add(java.nio.file.attribute.PosixFilePermission.GROUP_READ);
    if ((mode & 020) != 0) perms.add(java.nio.file.attribute.PosixFilePermission.GROUP_WRITE);
    if ((mode & 010) != 0) perms.add(java.nio.file.attribute.PosixFilePermission.GROUP_EXECUTE);
    if ((mode & 04) != 0) perms.add(java.nio.file.attribute.PosixFilePermission.OTHERS_READ);
    if ((mode & 02) != 0) perms.add(java.nio.file.attribute.PosixFilePermission.OTHERS_WRITE);
    if ((mode & 01) != 0) perms.add(java.nio.file.attribute.PosixFilePermission.OTHERS_EXECUTE);
    return perms;
  }

  private static int posixToMode(java.util.Set<java.nio.file.attribute.PosixFilePermission> perms) {
    int mode = 0;
    if (perms.contains(java.nio.file.attribute.PosixFilePermission.OWNER_READ)) mode |= 0400;
    if (perms.contains(java.nio.file.attribute.PosixFilePermission.OWNER_WRITE)) mode |= 0200;
    if (perms.contains(java.nio.file.attribute.PosixFilePermission.OWNER_EXECUTE)) mode |= 0100;
    if (perms.contains(java.nio.file.attribute.PosixFilePermission.GROUP_READ)) mode |= 040;
    if (perms.contains(java.nio.file.attribute.PosixFilePermission.GROUP_WRITE)) mode |= 020;
    if (perms.contains(java.nio.file.attribute.PosixFilePermission.GROUP_EXECUTE)) mode |= 010;
    if (perms.contains(java.nio.file.attribute.PosixFilePermission.OTHERS_READ)) mode |= 04;
    if (perms.contains(java.nio.file.attribute.PosixFilePermission.OTHERS_WRITE)) mode |= 02;
    if (perms.contains(java.nio.file.attribute.PosixFilePermission.OTHERS_EXECUTE)) mode |= 01;
    return mode;
  }

`)

	// directoryFromFileSystem
	b.WriteString("  /**\n")
	b.WriteString("   * Reads a real directory tree from disk and returns a populated Directory proto message.\n")
	b.WriteString("   * Skips symlinks, sockets, pipes, device files, and known system dotfiles.\n")
	b.WriteString("   */\n")
	b.WriteString("  public static " + outerClassName + ".Directory directoryFromFileSystem(java.nio.file.Path path) throws java.io.IOException {\n")
	b.WriteString("    if (!java.nio.file.Files.isDirectory(path)) {\n")
	b.WriteString("      throw new java.io.IOException(path + \" is not a directory\");\n")
	b.WriteString("    }\n")
	b.WriteString("    java.util.Set<java.nio.file.attribute.PosixFilePermission> dirPerms =\n")
	b.WriteString("        java.nio.file.Files.getPosixFilePermissions(path);\n")
	b.WriteString("    " + outerClassName + ".Directory.Builder dirBuilder = " + outerClassName + ".Directory.newBuilder()\n")
	b.WriteString("        .setName(path.getFileName().toString())\n")
	b.WriteString("        .setPermission(modeToPermission(posixToMode(dirPerms)));\n\n")
	b.WriteString(`    try (java.util.stream.Stream<java.nio.file.Path> stream = java.nio.file.Files.list(path)) {
      for (java.nio.file.Path entry : (Iterable<java.nio.file.Path>) stream::iterator) {
        if (shouldSkipEntry(entry)) {
          continue;
        }
        if (java.nio.file.Files.isDirectory(entry)) {
          dirBuilder.addDirectories(directoryFromFileSystem(entry));
        } else {
          java.util.Set<java.nio.file.attribute.PosixFilePermission> filePerms =
              java.nio.file.Files.getPosixFilePermissions(entry);
          byte[] content = java.nio.file.Files.readAllBytes(entry);
          dirBuilder.addFiles(`)
	b.WriteString(outerClassName + `.File.newBuilder()
              .setName(entry.getFileName().toString())
              .setPermission(modeToPermission(posixToMode(filePerms)))
              .setContent(com.google.protobuf.ByteString.copyFrom(content))
              .build());
        }
      }
    }
    return dirBuilder.build();
  }

`)

	// directoryToFileSystem
	b.WriteString("  /**\n")
	b.WriteString("   * Writes a Directory proto tree to disk, creating directories and files\n")
	b.WriteString("   * with the correct Unix permissions.\n")
	b.WriteString("   */\n")
	b.WriteString("  public static void directoryToFileSystem(" + outerClassName + ".Directory dir, java.nio.file.Path path) throws java.io.IOException {\n")
	b.WriteString(`    int mode = permissionToMode(dir.getPermission());
    java.nio.file.Files.createDirectories(path);
    java.nio.file.Files.setPosixFilePermissions(path, modeToPosix(mode));

    for (`)
	b.WriteString(outerClassName + `.File f : dir.getFilesList()) {
      java.nio.file.Path filePath = path.resolve(f.getName());
      int fileMode = permissionToMode(f.getPermission());
      java.nio.file.Files.write(filePath, f.getContent().toByteArray());
      java.nio.file.Files.setPosixFilePermissions(filePath, modeToPosix(fileMode));
    }

    for (`)
	b.WriteString(outerClassName + `.Directory sub : dir.getDirectoriesList()) {
      directoryToFileSystem(sub, path.resolve(sub.getName()));
    }
  }

`)
}
