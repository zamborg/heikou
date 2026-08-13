package brief

// Text normalization for a brief lives in internal/format, which owns the
// helpers every Heikou surface shares. A brief has the strongest reason of any
// caller to reuse it rather than fork: a configured command's output is
// untrusted, and "make this safe to print on one line" is exactly the problem
// that package already solved.
//
// maxTextRunes is the one thing that is local. A brief occupies part of one
// row; text beyond this cannot be shown and only costs memory to carry.
const maxTextRunes = 200
