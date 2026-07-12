import * as Headless from '@headlessui/react'
import React, { forwardRef } from 'react'
import { Link as RouterLink } from 'react-router'

export const Link = forwardRef(function Link(
  { href, ...props }: { href: string } & React.ComponentPropsWithoutRef<'a'>,
  ref: React.ForwardedRef<HTMLAnchorElement>
) {
  const internal = href.startsWith('/') && !href.startsWith('//')
  return (
    <Headless.DataInteractive>
      {internal
        ? <RouterLink {...props} to={href} ref={ref} />
        : <a {...props} href={href} ref={ref} />}
    </Headless.DataInteractive>
  )
})
