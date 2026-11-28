plugins {
    kotlin("jvm") version "2.2.21"
    kotlin("android") version "2.2.21" apply false
    id("com.android.library") version "8.11.1" apply false
}

kotlin {
    jvmToolchain(17)
    sourceSets.test { kotlin.srcDir("src/vectorTest/kotlin") }
}

dependencies {
    implementation(libs.noise)
    implementation(libs.ktor.http.cio)
    implementation(libs.coroutines)
    testImplementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.9.0")
    testImplementation("io.ktor:ktor-server-cio:3.3.2")
    testImplementation("io.ktor:ktor-server-websockets:3.3.2")
    testImplementation(kotlin("test-junit5"))
    testRuntimeOnly("org.junit.platform:junit-platform-launcher:1.11.4")
}

tasks.test {
    useJUnitPlatform()
    inputs.file("../testdata/wire-v1.json")
}
tasks.register("interopClasspath") {
    dependsOn(tasks.testClasses)
    doLast { println(sourceSets.test.get().runtimeClasspath.asPath) }
}
