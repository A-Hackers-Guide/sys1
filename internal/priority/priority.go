// Package priority drops the current process to the lowest scheduling
// priority the OS offers, so sys0 only ever uses CPU and disk time that
// nothing else wants. Child processes inherit it.
//
// Call Lowest as early as possible in main: on Linux, priority is per
// thread, and threads created later inherit it from their creator.
package priority
