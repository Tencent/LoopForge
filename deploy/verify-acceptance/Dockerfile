# JUnit acceptance runner for LoopForge /verify. The generated AegisOS specs
# import only the JDK and junit-jupiter-api, so the image needs no AegisOS
# source: JDK 21 + the pinned JUnit console launcher are enough. The base is
# the same toolchain image /verify already uses, so both gates share one pull.
FROM maven:3.9.11-eclipse-temurin-21

ARG JUNIT_CONSOLE_VERSION=1.10.2
RUN mkdir -p /opt/junit \
 && mvn -q org.apache.maven.plugins:maven-dependency-plugin:3.6.1:copy \
      -Dartifact=org.junit.platform:junit-platform-console-standalone:${JUNIT_CONSOLE_VERSION} \
      -DoutputDirectory=/opt/junit \
 && test -f /opt/junit/junit-platform-console-standalone-${JUNIT_CONSOLE_VERSION}.jar

COPY run.sh /opt/run.sh
RUN chmod +x /opt/run.sh
ENTRYPOINT ["/opt/run.sh"]
