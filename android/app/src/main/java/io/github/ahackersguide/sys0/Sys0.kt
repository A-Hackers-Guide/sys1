package io.github.ahackersguide.sys0

import android.content.Context
import android.os.Build
import java.io.File
import java.util.concurrent.TimeUnit

// Sys0 runs the bundled sys0 program. Android installs it with the app as
// lib/<abi>/libsys0.so, the one place an app may run programs from.
object Sys0 {
    fun binary(ctx: Context) = File(ctx.applicationInfo.nativeLibraryDir, "libsys0.so")
    fun dataDir(ctx: Context) = File(ctx.filesDir, "sys0").apply { mkdirs() }
    fun prefs(ctx: Context) = ctx.getSharedPreferences("sys0", Context.MODE_PRIVATE)

    fun joined(ctx: Context) = File(dataDir(ctx), "config.json").exists()
    fun enabled(ctx: Context) = prefs(ctx).getBoolean("enabled", true)
    fun setEnabled(ctx: Context, on: Boolean) = prefs(ctx).edit().putBoolean("enabled", on).apply()

    // run executes one sys0 command and returns its exit code and output.
    fun run(ctx: Context, vararg args: String, timeoutSeconds: Long = 60): Pair<Int, String> {
        val cmd = listOf(binary(ctx).absolutePath) + args + listOf("--data", dataDir(ctx).absolutePath)
        val p = ProcessBuilder(cmd).redirectErrorStream(true).start()
        val out = StringBuilder()
        val reader = Thread { p.inputStream.bufferedReader().forEachLine { out.appendLine(it) } }
        reader.start()
        if (!p.waitFor(timeoutSeconds, TimeUnit.SECONDS)) {
            p.destroyForcibly()
            return -1 to "sys0 took too long"
        }
        reader.join(1000)
        return p.exitValue() to out.toString().trim()
    }

    fun join(ctx: Context, code: String, mine: Boolean, storageGB: Int): Pair<Int, String> {
        val name = "${Build.MANUFACTURER} ${Build.MODEL}".trim()
        return run(ctx, "join", code, "--name", name, "--trust", if (mine) "owner" else "community",
            "--storage-gb", storageGB.toString(), "--no-service")
    }

    fun leave(ctx: Context) {
        File(dataDir(ctx), "config.json").delete()
    }

    fun status(ctx: Context): String = run(ctx, "status", timeoutSeconds = 15).second

    fun logTail(ctx: Context, lines: Int = 12): String {
        val f = File(dataDir(ctx), "sys0.log")
        if (!f.exists()) return ""
        return f.readLines().takeLast(lines).joinToString("\n")
    }
}
