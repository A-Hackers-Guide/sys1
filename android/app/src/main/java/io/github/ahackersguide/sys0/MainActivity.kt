package io.github.ahackersguide.sys0

import android.Manifest
import android.app.Activity
import android.content.pm.PackageManager
import android.graphics.Color
import android.graphics.Typeface
import android.os.Build
import android.os.Bundle
import android.text.InputType
import android.view.Gravity
import android.view.View
import android.view.ViewGroup.LayoutParams.MATCH_PARENT
import android.view.ViewGroup.LayoutParams.WRAP_CONTENT
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.RadioButton
import android.widget.RadioGroup
import android.widget.ScrollView
import android.widget.Switch
import android.widget.TextView

// MainActivity is sys0's whole interface on Android: join a pool, see what
// it's doing, pause it, or leave. Built in code, so it works the same on
// phones, tablets and TVs without layout files.
class MainActivity : Activity() {
    private lateinit var root: LinearLayout

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }
        root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(24), dp(32), dp(24), dp(32))
        }
        setContentView(ScrollView(this).apply { setBackgroundColor(Color.parseColor("#0F1A17")); addView(root) })
        render()
    }

    override fun onResume() {
        super.onResume()
        render()
    }

    private fun dp(v: Int) = (v * resources.displayMetrics.density).toInt()

    private fun text(s: String, size: Float = 16f, bold: Boolean = false, color: String = "#E8EFEC") =
        TextView(this).apply {
            text = s
            textSize = size
            setTextColor(Color.parseColor(color))
            if (bold) typeface = Typeface.DEFAULT_BOLD
            setPadding(0, dp(6), 0, dp(6))
        }

    private fun button(s: String, onClick: () -> Unit) = Button(this).apply {
        text = s
        isAllCaps = false
        setOnClickListener { onClick() }
    }

    private fun render() {
        root.removeAllViews()
        root.addView(text("sys0", 32f, bold = true, color = "#3DD6A3"))
        if (Sys0.joined(this)) renderJoined() else renderSetup()
    }

    private fun renderSetup() {
        root.addView(text("Lend this device's spare power and storage to a sys1 pool. It only works while the device is charging and you're not using it, and stops the moment you unplug or pick it up."))
        root.addView(text("Join code", bold = true))
        root.addView(text("On your node's dashboard, open Add a machine and copy the join code.", 14f, color = "#93A39D"))
        val code = EditText(this).apply {
            hint = "sys1-join:…"
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS
            setTextColor(Color.WHITE)
            setHintTextColor(Color.parseColor("#93A39D"))
        }
        root.addView(code, LinearLayout.LayoutParams(MATCH_PARENT, WRAP_CONTENT))

        root.addView(text("Whose device is this?", bold = true))
        val shared = RadioButton(this).apply { text = "I'm sharing it with someone else's pool (safe choice)"; setTextColor(Color.WHITE); id = View.generateViewId(); isChecked = true }
        val mine = RadioButton(this).apply { text = "Mine. I run the node too."; setTextColor(Color.WHITE); id = View.generateViewId() }
        root.addView(RadioGroup(this).apply { addView(shared); addView(mine) })

        root.addView(text("Storage to lend (GB)", bold = true))
        val gb = EditText(this).apply { setText("5"); inputType = InputType.TYPE_CLASS_NUMBER; setTextColor(Color.WHITE) }
        root.addView(gb, LinearLayout.LayoutParams(dp(120), WRAP_CONTENT))

        val error = text("", 14f, color = "#FF6B7A")
        val join = button("Join the pool") {}
        join.setOnClickListener {
            join.isEnabled = false
            error.text = "Joining…"
            val c = code.text.toString().trim()
            val size = gb.text.toString().toIntOrNull() ?: 5
            Thread {
                val (exit, out) = Sys0.join(this, c, mine.isChecked, size)
                runOnUiThread {
                    join.isEnabled = true
                    if (exit == 0) {
                        Sys0.setEnabled(this, true)
                        Sys0Service.start(this)
                        render()
                    } else {
                        error.text = out.lines().lastOrNull { it.isNotBlank() } ?: "Couldn't join"
                    }
                }
            }.start()
        }
        root.addView(join)
        root.addView(error)
    }

    private fun renderJoined() {
        val status = text("Loading…", 14f, color = "#93A39D").apply { typeface = Typeface.MONOSPACE }
        root.addView(text("Joined", 20f, bold = true))
        root.addView(text("sys0 lends this device's spare power while it's charging and idle${if (packageManager.hasSystemFeature(PackageManager.FEATURE_LEANBACK)) " (on a TV: whenever it's on power)" else ""}."))
        val toggle = Switch(this).apply {
            text = "Lend spare power"
            setTextColor(Color.WHITE)
            isChecked = Sys0.enabled(this@MainActivity)
            setOnCheckedChangeListener { _, on ->
                Sys0.setEnabled(this@MainActivity, on)
                if (on) Sys0Service.start(this@MainActivity) else Sys0Service.stop(this@MainActivity)
            }
        }
        root.addView(toggle)
        root.addView(status)
        root.addView(button("Leave the pool") {
            Sys0Service.stop(this)
            Sys0.leave(this)
            render()
        })
        root.addView(text("Recent activity", bold = true))
        val log = text("", 12f, color = "#93A39D").apply { typeface = Typeface.MONOSPACE; gravity = Gravity.START }
        root.addView(log)
        Thread {
            val s = Sys0.status(this)
            val l = Sys0.logTail(this)
            runOnUiThread { status.text = s; log.text = l.ifEmpty { "Nothing yet." } }
        }.start()
        Sys0Service.start(this)
    }
}
