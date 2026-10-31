# Port for the FreeBSD ports tree (sysutils/sys1). Builds from source.
PORTNAME=	sys1
DISTVERSIONPREFIX=	v
DISTVERSION=	0.1.0
CATEGORIES=	sysutils net

MAINTAINER=	289517818+A-Hackers-Guide@users.noreply.github.com
COMMENT=	Pool the spare power of every computer into one cloud
WWW=		https://a-hackers-guide.github.io/sys1/

LICENSE=	UNLICENSE
LICENSE_NAME=	The Unlicense
LICENSE_FILE=	${WRKSRC}/LICENSE
LICENSE_PERMS=	dist-mirror dist-sell pkg-mirror pkg-sell auto-accept

USES=		go:modules
USE_GITHUB=	yes
GH_ACCOUNT=	A-Hackers-Guide

GO_TARGET=	./cmd/sys0 ./cmd/sys1
GO_LDFLAGS=	-s -w -X github.com/A-Hackers-Guide/sys1/internal/version.Version=${DISTVERSION}

PLIST_FILES=	bin/sys0 bin/sys1

.include <bsd.port.mk>
