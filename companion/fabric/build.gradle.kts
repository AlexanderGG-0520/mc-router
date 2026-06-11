dependencies {
    api(project(":core"))
    compileOnly("net.fabricmc:fabric-loader:0.18.4")
    compileOnly("de.maxhenkel.voicechat:voicechat-api:2.6.13")
}

tasks.jar {
    archiveBaseName.set("mc-router-voicechat-companion-fabric")
    from(project(":core").sourceSets.main.get().output)
}

val companionVersion = version

tasks.processResources {
    filesMatching("fabric.mod.json") {
        expand("version" to companionVersion)
    }
}

java {
    toolchain {
        languageVersion.set(JavaLanguageVersion.of(25))
    }
}

tasks.withType<JavaCompile>().configureEach {
    options.release.set(25)
}
