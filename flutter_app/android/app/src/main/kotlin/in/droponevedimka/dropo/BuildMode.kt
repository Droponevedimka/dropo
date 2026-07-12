package `in`.droponevedimka.dropo

import android.content.Context
import android.content.pm.ApplicationInfo

internal fun Context.isDebugBuild(): Boolean =
    applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE != 0
