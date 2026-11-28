plugins {
    id("com.android.library")
    kotlin("android")
}

android {
    namespace = "arcships.rcp"
    compileSdk = 34
    defaultConfig {
        minSdk = 29
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    sourceSets["androidTest"].java.srcDir("../src/vectorTest/kotlin")
    sourceSets["androidTest"].assets.srcDir("../../testdata")
    sourceSets["main"].java.srcDir("../src/main/kotlin")
}

kotlin { jvmToolchain(17) }

dependencies {
    androidTestImplementation(kotlin("test-junit"))
    androidTestImplementation("androidx.test:runner:1.6.2")
    androidTestImplementation("androidx.test.ext:junit:1.2.1")
    androidTestImplementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.9.0")
    implementation(libs.noise)
    implementation(libs.ktor.http.cio)
    implementation(libs.coroutines)
}
