---
"ftw": patch
---

A history write that is stopped or runs out of time now keeps other history writes waiting until SQLite has rolled it back. This covers the control loop's write, the import of older history and the import of plain archive files. Before, the next write could start too early and then wait or fail as busy. No data was lost either way.
