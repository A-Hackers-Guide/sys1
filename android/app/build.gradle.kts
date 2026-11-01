// The sys0 Android app. The real work is done by the sys0 program itself,
// built by Go for each CPU and bundled as jniLibs/<abi>/libsys0.so (see
// .github/workflows/android.yml); this app sets it up and runs it.
plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "io.github.ahackersguide.sys0"
    compileSdk = 35

    defaultConfig {
        applicationId = "io.github.ahackersguide.sys0"
        minSdk = 26
        targetSdk = 35
        versionCode = (project.findProperty("sys1VersionCode") as String?)?.toInt() ?: 1
        versionName = (project.findProperty("sys1Version") as String?) ?: "dev"
    }

    signingConfigs {
        create("release") {
            val ks = System.getenv("SYS1_KEYSTORE")
            if (ks != null) {
                storeFile = file(ks)
                storePassword = System.getenv("SYS1_KEYSTORE_PASSWORD")
                keyAlias = System.getenv("SYS1_KEY_ALIAS")
                keyPassword = System.getenv("SYS1_KEYSTORE_PASSWORD")
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            if (System.getenv("SYS1_KEYSTORE") != null) {
                signingConfig = signingConfigs.getByName("release")
            }
        }
    }

    packaging {
        // sys0 must be extracted to disk so it can be run as a program.
        jniLibs.useLegacyPackaging = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}
