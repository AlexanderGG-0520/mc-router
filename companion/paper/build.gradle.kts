dependencies {
    api(project(":core"))
    compileOnly("io.papermc.paper:paper-api:1.21.8-R0.1-SNAPSHOT")
    compileOnly("de.maxhenkel.voicechat:voicechat-api:2.6.20")
}

tasks.jar {
    archiveBaseName.set("mc-router-voicechat-companion-paper")
    from(project(":core").sourceSets.main.get().output)
}

val companionVersion = version

tasks.processResources {
    filesMatching("paper-plugin.yml") {
        expand("version" to companionVersion)
    }
}
