import java.io.FileInputStream
import java.util.Properties

plugins {
    id("com.android.application")
    // The Flutter Gradle Plugin must be applied after the Android and Kotlin Gradle plugins.
    id("dev.flutter.flutter-gradle-plugin")
}

val releaseSigningProperties = Properties()
val releaseSigningPropertiesFile = System.getenv("DROPO_ANDROID_SIGNING_PROPERTIES")
    ?.takeIf { it.isNotBlank() }
    ?.let(::file)
    ?: file("${System.getProperty("user.home")}/.dropo-signing/android-signing.properties")
if (releaseSigningPropertiesFile.isFile) {
    FileInputStream(releaseSigningPropertiesFile).use(releaseSigningProperties::load)
}

fun releaseSigningValue(environmentName: String, propertyName: String): String? =
    System.getenv(environmentName)?.takeIf { it.isNotBlank() }
        ?: releaseSigningProperties.getProperty(propertyName)?.takeIf { it.isNotBlank() }

val releaseKeystorePath = releaseSigningValue("DROPO_ANDROID_KEYSTORE_PATH", "storeFile")
val releaseStorePassword = releaseSigningValue("DROPO_ANDROID_STORE_PASSWORD", "storePassword")
val releaseKeyAlias = releaseSigningValue("DROPO_ANDROID_KEY_ALIAS", "keyAlias")
val releaseKeyPassword = releaseSigningValue("DROPO_ANDROID_KEY_PASSWORD", "keyPassword")
val releaseSigningReady = listOf(
    releaseKeystorePath,
    releaseStorePassword,
    releaseKeyAlias,
    releaseKeyPassword,
).all { !it.isNullOrBlank() } && file(releaseKeystorePath!!).isFile
val releaseBuildRequested = gradle.startParameter.taskNames.any { it.contains("release", ignoreCase = true) }

if (releaseBuildRequested && !releaseSigningReady) {
    throw GradleException(
        "Android release signing is required. Set DROPO_ANDROID_SIGNING_* variables " +
            "or provide ${releaseSigningPropertiesFile.absolutePath}.",
    )
}

val flutterTargetPlatforms = (findProperty("target-platform") as? String)
    ?.split(",")
    ?.map { it.trim() }
    ?.filter { it.isNotEmpty() }
    ?: listOf("android-arm64")

val targetAbis = flutterTargetPlatforms.mapNotNull {
    when (it) {
        "android-arm" -> "armeabi-v7a"
        "android-arm64" -> "arm64-v8a"
        "android-x86" -> "x86"
        "android-x64" -> "x86_64"
        else -> null
    }
}.distinct()

android {
    namespace = "in.droponevedimka.dropo"
    compileSdk = 36
    ndkVersion = flutter.ndkVersion

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    defaultConfig {
        applicationId = "in.droponevedimka.dropo"
        minSdk = 30
        targetSdk = 36
        versionCode = flutter.versionCode
        versionName = flutter.versionName
        ndk {
            abiFilters += targetAbis
        }
    }

    signingConfigs {
        if (releaseSigningReady) {
            create("release") {
                storeFile = file(releaseKeystorePath!!)
                storePassword = releaseStorePassword
                keyAlias = releaseKeyAlias
                keyPassword = releaseKeyPassword
            }
        }
    }

    buildTypes {
        release {
            if (releaseSigningReady) {
                signingConfig = signingConfigs.getByName("release")
            }
        }
    }
}

dependencies {
    implementation(files("libs/dropoandroid.aar"))
}

kotlin {
    compilerOptions {
        jvmTarget = org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17
    }
}

flutter {
    source = "../.."
}
