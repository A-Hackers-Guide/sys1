package io.github.ahackersguide.sys0

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.os.BatteryManager
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.PowerManager
import java.util.concurrent.TimeUnit

// Sys0Service decides when this device lends its power, and runs sys0
// then. Phones and tablets lend only while charging with the screen off,
// and stop the moment either changes. TVs are always plugged in, so they
// lend whenever they are on power. Android requires a visible notification
// for this, which also makes it clear when sys0 is working.
class Sys0Service : Service() {
    private val handler = Handler(Looper.getMainLooper())
    private var proc: Process? = null
    private var crashes = 0

    private val watcher = object : BroadcastReceiver() {
        override fun onReceive(c: Context, i: Intent) = evaluate()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(CHANNEL, "sys0", NotificationManager.IMPORTANCE_MIN))
        val f = IntentFilter().apply {
            addAction(Intent.ACTION_POWER_CONNECTED)
            addAction(Intent.ACTION_POWER_DISCONNECTED)
            addAction(Intent.ACTION_SCREEN_ON)
            addAction(Intent.ACTION_SCREEN_OFF)
        }
        if (Build.VERSION.SDK_INT >= 33) registerReceiver(watcher, f, RECEIVER_NOT_EXPORTED) else registerReceiver(watcher, f)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (!Sys0.joined(this) || !Sys0.enabled(this)) {
            stopSelf()
            return START_NOT_STICKY
        }
        val n = notification("Waiting until this device is charging and idle")
        if (Build.VERSION.SDK_INT >= 34) startForeground(ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE) else startForeground(ID, n)
        evaluate()
        return START_STICKY
    }

    override fun onDestroy() {
        unregisterReceiver(watcher)
        handler.removeCallbacksAndMessages(null)
        stopSys0()
        super.onDestroy()
    }

    private fun isTV() = packageManager.hasSystemFeature(PackageManager.FEATURE_LEANBACK)

    private fun charging(): Boolean {
        val b = registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED)) ?: return isTV()
        val plugged = b.getIntExtra(BatteryManager.EXTRA_PLUGGED, 0)
        return plugged != 0 || isTV()
    }

    private fun idle(): Boolean = isTV() || !getSystemService(PowerManager::class.java).isInteractive

    // evaluate starts or stops sys0 to match the device's state.
    private fun evaluate() {
        val want = Sys0.joined(this) && Sys0.enabled(this) && charging() && idle()
        if (want) startSys0() else stopSys0()
        val text = when {
            want -> "Lending spare power to your pool"
            !charging() -> "Paused: plug in to lend spare power"
            else -> "Paused while you use this device"
        }
        getSystemService(NotificationManager::class.java).notify(ID, notification(text))
    }

    private fun startSys0() {
        if (proc?.isAlive == true) return
        val p = ProcessBuilder(Sys0.binary(this).absolutePath, "run", "--data", Sys0.dataDir(this).absolutePath)
            .redirectErrorStream(true)
            .redirectOutput(java.io.File("/dev/null")) // sys0 keeps its own log
            .start()
        proc = p
        Thread {
            val started = System.currentTimeMillis()
            p.waitFor()
            handler.post {
                if (proc !== p) return@post // stopped on purpose
                proc = null
                // Restart with backoff, like sys0's own watchdog.
                crashes = if (System.currentTimeMillis() - started > 60_000) 1 else crashes + 1
                val delay = (1000L shl minOf(crashes, 5)).coerceAtMost(30_000)
                handler.postDelayed({ evaluate() }, delay)
            }
        }.start()
    }

    private fun stopSys0() {
        val p = proc ?: return
        proc = null
        p.destroy() // SIGTERM: sys0 stops its tasks and saves what it must
        Thread {
            if (!p.waitFor(20, TimeUnit.SECONDS)) p.destroyForcibly()
        }.start()
    }

    private fun notification(text: String): Notification {
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        return Notification.Builder(this, CHANNEL)
            .setSmallIcon(android.R.drawable.stat_notify_sync_noanim)
            .setContentTitle("sys0")
            .setContentText(text)
            .setContentIntent(open)
            .setOngoing(true)
            .build()
    }

    companion object {
        const val CHANNEL = "sys0"
        const val ID = 1

        fun start(ctx: Context) {
            if (Sys0.joined(ctx) && Sys0.enabled(ctx)) ctx.startForegroundService(Intent(ctx, Sys0Service::class.java))
        }

        fun stop(ctx: Context) = ctx.stopService(Intent(ctx, Sys0Service::class.java))
    }
}

// BootReceiver brings the service back after a restart or an update.
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(c: Context, i: Intent) = Sys0Service.start(c)
}
