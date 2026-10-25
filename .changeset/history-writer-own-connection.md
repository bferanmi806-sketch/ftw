---
"ftw": patch
---

When the import of older history or of plain archive files is stopped or runs out of time, other history writes now wait until SQLite has rolled it back. Before, the next write could start too early and then wait or fail as busy. No data was lost either way.
